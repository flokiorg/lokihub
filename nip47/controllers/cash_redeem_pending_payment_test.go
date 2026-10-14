package controllers

import (
	"context"
	"fmt"
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

// redeemWithUnknownOutcome runs a cash_redeem whose payment returns
// lnclient.ErrPaymentPending, the error a broken payment stream produces while
// the HTLC may still be in flight.
func redeemWithUnknownOutcome(t *testing.T) (*tests.TestService, *db.App, string) {
	t.Helper()
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
	mockLn.PayInvoiceErrors = append(mockLn.PayInvoiceErrors,
		fmt.Errorf("%w: rpc error: code = Canceled desc = context canceled", lnclient.ErrPaymentPending))
	mockLn.PayInvoiceResponses = append(mockLn.PayInvoiceResponses, nil)

	resp := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, params)
	require.NotNil(t, resp.Error, "an unknown outcome is not reported as success")

	var pending db.Transaction
	require.NoError(t, svc.DB.Where(&db.Transaction{
		Type: constants.TRANSACTION_TYPE_OUTGOING, PaymentHash: tests.MockZeroAmountPaymentHash,
	}).First(&pending).Error)
	require.Equal(t, constants.TRANSACTION_STATE_PENDING, pending.State, "an unknown outcome is not marked failed")

	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, claimantPubkey)
	require.NoError(t, err)
	require.Nil(t, claim, "the slice stays claimed while its payment may still settle")

	return svc, wallet, claimantPubkey
}

// CASH-redeeming-a-slice-12: a failure after payment begins MUST NOT leave the
// slice redeemable a second time.
func TestHandleCashRedeemEvent_UnknownOutcomeThenLateSettle_NotRedeemableAgain(t *testing.T) {
	svc, wallet, claimantPubkey := redeemWithUnknownOutcome(t)

	transactions.NewTransactionsService(svc.DB, svc.EventPublisher).ConsumeEvent(context.TODO(), &events.Event{
		Event: "nwc_lnclient_payment_sent",
		Properties: &lnclient.Transaction{
			Type: "outgoing", Invoice: tests.MockZeroAmountInvoice, Preimage: tests.RandomHex32(),
			PaymentHash: tests.MockZeroAmountPaymentHash, Amount: 1000,
		},
	}, nil)

	var settled db.Transaction
	require.NoError(t, svc.DB.Where(&db.Transaction{
		Type: constants.TRANSACTION_TYPE_OUTGOING, PaymentHash: tests.MockZeroAmountPaymentHash,
		State: constants.TRANSACTION_STATE_SETTLED,
	}).First(&settled).Error, "the payout settled")

	released, err := svc.AppsService.ReleaseCashSliceForFailedPayment(wallet.ID, tests.MockZeroAmountPaymentHash)
	require.NoError(t, err)
	require.False(t, released, "a stray failure for a settled payout must not reopen the slice")

	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, claimantPubkey)
	require.Error(t, err, "a slice whose payout settled can't be redeemed again")
}

func TestHandleCashRedeemEvent_UnknownOutcomeThenLateFailure_SliceReleased(t *testing.T) {
	svc, wallet, claimantPubkey := redeemWithUnknownOutcome(t)

	transactions.NewTransactionsService(svc.DB, svc.EventPublisher).ConsumeEvent(context.TODO(), &events.Event{
		Event: "nwc_lnclient_payment_failed",
		Properties: &lnclient.PaymentFailedEventProperties{
			Transaction: &lnclient.Transaction{PaymentHash: tests.MockZeroAmountPaymentHash},
			Reason:      "FAILURE_REASON_NO_ROUTE",
		},
	}, nil)

	var failed db.Transaction
	require.NoError(t, svc.DB.Where(&db.Transaction{
		Type: constants.TRANSACTION_TYPE_OUTGOING, PaymentHash: tests.MockZeroAmountPaymentHash,
		State: constants.TRANSACTION_STATE_FAILED,
	}).First(&failed).Error)

	// What the nwc_payment_failed consumer does with that row.
	released, err := svc.AppsService.ReleaseCashSliceForFailedPayment(*failed.AppId, failed.PaymentHash)
	require.NoError(t, err)
	require.True(t, released)

	amount, err := svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, claimantPubkey)
	require.NoError(t, err, "a definitely failed payout leaves the slice redeemable")
	require.Equal(t, int64(1000), amount)
}
