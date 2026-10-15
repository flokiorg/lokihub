package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// A node that reports a payout as succeeded but returns no preimage has still
// paid it. cash_redeem used to read the resulting record error as a failed
// payment and release the slice, so the same slice could be redeemed again.
func TestHandleCashRedeemEvent_SentWithoutPreimage_SliceStaysClaimed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000},
	}))

	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())
	params := nipcash.CashRedeemRequest{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000),
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	}

	mockLn := svc.LNClient.(*tests.MockLn)
	mockLn.PayInvoiceResponses = append(mockLn.PayInvoiceResponses, &lnclient.PayInvoiceResponse{Preimage: ""})
	mockLn.PayInvoiceErrors = append(mockLn.PayInvoiceErrors, nil)

	controller := NewTestNip47Controller(svc)
	resp := handleClaimFundsFor(t, svc, controller, wallet, params)
	require.NotNil(t, resp.Error, "a payout that could not be recorded is not reported as success")
	require.Equal(t, constants.ERROR_INTERNAL, resp.Error.Code)

	var pending db.Transaction
	require.NoError(t, svc.DB.Where(&db.Transaction{
		Type: constants.TRANSACTION_TYPE_OUTGOING, PaymentHash: tests.MockZeroAmountPaymentHash,
	}).First(&pending).Error)
	require.Equal(t, constants.TRANSACTION_STATE_PENDING, pending.State,
		"a payout the node reported as sent is not marked failed")

	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, claimantPubkey)
	require.NoError(t, err)
	require.Nil(t, claim, "the slice stays claimed: its payout already left")

	callsBefore := len(mockLn.SendPaymentSyncCalls())
	again := handleClaimFundsFor(t, svc, controller, wallet, params)
	require.NotNil(t, again.Error, "a second redeem of the same slice is refused")
	require.Len(t, mockLn.SendPaymentSyncCalls(), callsBefore, "the second redeem never reaches the node")

	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, claimantPubkey)
	require.Error(t, err, "nothing unclaimed the slice")

	// Once the node's payment subscription reports the preimage, the row settles
	// and a stray failure can no longer reopen the slice.
	transactions.NewTransactionsService(svc.DB, svc.EventPublisher).ConsumeEvent(context.TODO(), &events.Event{
		Event: "nwc_lnclient_payment_sent",
		Properties: &lnclient.Transaction{
			Type: "outgoing", Invoice: tests.MockZeroAmountInvoice, Preimage: tests.RandomHex32(),
			PaymentHash: tests.MockZeroAmountPaymentHash, Amount: 1000,
		},
	}, nil)
	require.NoError(t, svc.DB.Where(&db.Transaction{
		Type: constants.TRANSACTION_TYPE_OUTGOING, PaymentHash: tests.MockZeroAmountPaymentHash,
		State: constants.TRANSACTION_STATE_SETTLED,
	}).First(&db.Transaction{}).Error, "the late preimage settles the payout")
	released, err := svc.AppsService.ReleaseCashSliceForFailedPayment(wallet.ID, tests.MockZeroAmountPaymentHash)
	require.NoError(t, err)
	require.False(t, released)
}
