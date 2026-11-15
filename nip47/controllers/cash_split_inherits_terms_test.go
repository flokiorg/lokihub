package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Data Model: a slice created by a split inherits its floor and
// redeem fee from the slice it was split from, never the Hub's current
// defaults. Otherwise a holder could shed a fee by splitting after the Hub
// lowered it. The new wallets also record which wallet they came from.
func TestHandleCashTransferEvent_PartialSplit_InheritsSourceTermsAndRecordsSource(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600) // the hub's own defaults are all 0
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, wallet.ID, 200_000, tests.RandomHex32())
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-carved", Amount: 2000},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, PaymentHash: tests.MockLNClientHoldTransaction.PaymentHash, Preimage: "preimage-remainder", Amount: 3000},
	}

	const floor, feePpm, feeBase = int64(1000), 5_000, int64(500)
	ownerPriv := nostr.GeneratePrivateKey()
	owner, _ := nostr.GetPublicKey(ownerPriv)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{{
		IdentityType: db.CashIdentityPubkey, IdentityValue: owner, AmountMloki: 5000,
		MinTransferMloki: floor, RedeemFeePpm: feePpm, RedeemFeeBaseMloki: feeBase,
	}}))

	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	amount := uint64(2000)
	proof := buildTransferProofEvent(t, ownerPriv, *wallet.WalletPubkey, db.CashIdentityPubkey, newPub, "", amount, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: owner,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
		AmountMillis:  &amount,
	})
	require.Nil(t, response.Error, "%+v", response.Error)
	result := response.Result.(cashTransferResponse)

	for _, walletPubkey := range []string{result.NewWalletPubkey, result.RemainderWalletPubkey} {
		var child db.App
		require.NoError(t, svc.DB.Where("wallet_pubkey = ?", walletPubkey).First(&child).Error)
		require.NotNil(t, child.SplitFromWalletAppID)
		assert.Equal(t, wallet.ID, *child.SplitFromWalletAppID, "a split-off wallet records the wallet it came from")

		var slice db.CashWalletClaim
		require.NoError(t, svc.DB.Where("wallet_app_id = ?", child.ID).First(&slice).Error)
		assert.Equal(t, floor, slice.MinTransferMloki)
		assert.Equal(t, feePpm, slice.RedeemFeePpm)
		assert.Equal(t, feeBase, slice.RedeemFeeBaseMloki)
	}
}
