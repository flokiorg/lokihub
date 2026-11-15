package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// A cash_redeem payout whose outcome the node reached while lokihub was down is
// never sent by the payment subscription. Reconciliation asks the node directly:
// settled stays redeemed, failed releases the slice, unknown is left alone.
func TestReconcilePendingCashRedeems_AfterRestart(t *testing.T) {
	payoutState := func(t *testing.T, svc *tests.TestService) string {
		t.Helper()
		var payout db.Transaction
		require.NoError(t, svc.DB.Where(&db.Transaction{
			Type: constants.TRANSACTION_TYPE_OUTGOING, PaymentHash: tests.MockZeroAmountPaymentHash,
		}).First(&payout).Error)
		return payout.State
	}

	t.Run("settled", func(t *testing.T) {
		svc, wallet, claimant := redeemWithUnknownOutcome(t)
		svc.LNClient.(*tests.MockLn).PaymentLookups = map[string]*lnclient.PaymentLookup{
			tests.MockZeroAmountPaymentHash: {Status: lnclient.PaymentStatusSucceeded, Preimage: tests.RandomHex32()},
		}

		unresolved := transactions.NewTransactionsService(svc.DB, svc.EventPublisher).ReconcilePendingCashRedeems(context.TODO(), svc.LNClient)
		assert.Zero(t, unresolved)
		assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, payoutState(t, svc))
		_, err := svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, claimant)
		require.Error(t, err, "a settled payout keeps its slice redeemed")
	})

	t.Run("failed", func(t *testing.T) {
		svc, wallet, claimant := redeemWithUnknownOutcome(t)
		svc.LNClient.(*tests.MockLn).PaymentLookups = map[string]*lnclient.PaymentLookup{
			tests.MockZeroAmountPaymentHash: {Status: lnclient.PaymentStatusFailed, FailureReason: "FAILURE_REASON_NO_ROUTE"},
		}

		unresolved := transactions.NewTransactionsService(svc.DB, svc.EventPublisher).ReconcilePendingCashRedeems(context.TODO(), svc.LNClient)
		assert.Zero(t, unresolved)
		assert.Equal(t, constants.TRANSACTION_STATE_FAILED, payoutState(t, svc))

		// What the nwc_payment_failed consumer does with it.
		released, err := svc.AppsService.ReleaseCashSliceForFailedPayment(wallet.ID, tests.MockZeroAmountPaymentHash)
		require.NoError(t, err)
		require.True(t, released)
		_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, claimant)
		require.NoError(t, err, "a definitely failed payout leaves the slice redeemable")
	})

	t.Run("still unknown", func(t *testing.T) {
		svc, wallet, claimant := redeemWithUnknownOutcome(t)

		unresolved := transactions.NewTransactionsService(svc.DB, svc.EventPublisher).ReconcilePendingCashRedeems(context.TODO(), svc.LNClient)
		assert.Equal(t, 1, unresolved)
		assert.Equal(t, constants.TRANSACTION_STATE_PENDING, payoutState(t, svc))
		claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, claimant)
		require.NoError(t, err)
		assert.Nil(t, claim, "the slice stays claimed while the outcome is unknown")
	})
}
