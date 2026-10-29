package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// CASH-redeeming-a-slice-06: the identity_event MUST bind the caller to that slice's
// current registered identity. On a shared bill, a co-recipient's proof is correctly
// bound to the wallet and the invoice, so the pubkey-mode signer check is the only
// thing stopping them from redeeming someone else's slice.
func TestHandleCashRedeemEvent_CoRecipientSignsForAnotherSlice_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 2000)

	alicePubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	bobPrivkey := nostr.GeneratePrivateKey()
	bobPubkey, _ := nostr.GetPublicKey(bobPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: alicePubkey, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: bobPubkey, AmountMloki: 1000},
	}))

	// Bob's own key, bound to this wallet and this invoice, naming Alice's slice.
	proof := buildClaimProofEvent(t, bobPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())
	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, nipcash.CashRedeemRequest{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000),
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: alicePubkey,
		IdentityEvent: mustMarshal(t, proof),
	})

	require.NotNil(t, response.Error, "a co-recipient must not redeem another recipient's slice")
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, response.Error.Code)

	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, alicePubkey)
	require.NoError(t, err)
	require.NotNil(t, claim, "Alice's slice stays unclaimed")

	var payouts int64
	require.NoError(t, svc.DB.Model(&db.Transaction{}).
		Where("app_id = ? AND type = ?", wallet.ID, constants.TRANSACTION_TYPE_OUTGOING).Count(&payouts).Error)
	assert.Zero(t, payouts, "no payment was attempted")
}
