package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Lifecycle and Deletion: the auto-delete policy MUST be the same for
// cash_redeem, cash_transfer and cash_consolidate (CASH-lifecycle-and-deletion-
// 06/-07). Redeem is pinned by TestHandleCashRedeemEvent_DrainedBillIsArchivedAndDeleted;
// these are the other two, on their success paths.

func TestHandleCashTransferEvent_SpinOff_DrainedSourceIsDeleted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const sliceMloki = 123_000 // tests.MockInvoice's fixed amount
	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", sliceMloki/1000, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, wallet.ID, sliceMloki, tests.RandomHex32())
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-spinoff", Amount: sliceMloki},
	}

	ownerPriv := nostr.GeneratePrivateKey()
	owner, _ := nostr.GetPublicKey(ownerPriv)
	other, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: owner, AmountMloki: sliceMloki},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: other, AmountMloki: 1000},
	}))
	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, other)
	require.NoError(t, err)

	_, newSecretHash := cashSecretAndHash(t)
	proof := buildTransferProofEvent(t, ownerPriv, *wallet.WalletPubkey, db.CashIdentityCash, newSecretHash, "", sliceMloki, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: owner,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityCash, IdentityValue: newSecretHash},
	})
	require.Nil(t, response.Error, "%+v", response.Error)

	requireBillArchivedAndDeleted(t, svc, wallet.ID)
}

func TestHandleCashConsolidateEvent_DrainedSourceIsDeleted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)
	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)
	_, secretHash, err := cashwallet.GenerateCashSecret()
	require.NoError(t, err)

	const amount1, amount2 = uint64(123_000), uint64(1_000)
	drained := guardSource(t, svc, hub, ownerPub, amount1, 0, 0)
	tests.FundApp(svc, drained.ID, amount1, "drained-fund") // exactly its slice
	topped := guardSource(t, svc, hub, ownerPub, amount2, 0, 0)
	tests.FundApp(svc, topped.ID, 200_000, "topped-fund") // more than its slice

	mockLN := svc.LNClient.(*tests.MockLn)
	// MockInvoice's payee, so the exactly-funded source's transfer settles as a
	// fee-free self-payment and leaves it at zero.
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "p1", Amount: int64(amount1)},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, PaymentHash: tests.MockLNClientHoldTransaction.PaymentHash, Preimage: "p2", Amount: int64(amount2)},
	}
	proof1 := buildTransferProofEvent(t, ownerPriv, *drained.WalletPubkey, db.CashIdentityCash, secretHash, "", amount1, nil, time.Now())
	proof2 := buildTransferProofEvent(t, ownerPriv, *topped.WalletPubkey, db.CashIdentityCash, secretHash, "", amount2, nil, time.Now())
	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *drained.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof1)},
			{WalletPubkey: *topped.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof2)},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityCash, IdentityValue: secretHash},
	})
	require.Nil(t, resp.Error, "%+v", resp.Error)

	requireBillArchivedAndDeleted(t, svc, drained.ID)
	var live int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", topped.ID).Count(&live).Error)
	assert.EqualValues(t, 1, live, "a source still holding funds is not deleted")
}
