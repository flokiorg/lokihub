package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/tests"
)

func TestCashRedeemFailureConsumer_ReleasesHeldSliceOnlyForItsFailedPayment(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	const paymentHash = "8c4859ba70ed96328bec21b6c2f97d5453dc8c88bc56533209711701a8ff4211"
	claimedAt := time.Now()
	claim := db.CashWalletClaim{
		WalletAppID: hub.ID, IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(),
		AmountMloki: 1000, ClaimedAt: &claimedAt, PaymentHash: paymentHash,
	}
	require.NoError(t, svc.DB.Create(&claim).Error)
	payment := db.Transaction{
		AppId: &hub.ID, Type: constants.TRANSACTION_TYPE_OUTGOING, State: constants.TRANSACTION_STATE_FAILED,
		PaymentHash: paymentHash, AmountMloki: 1000,
	}
	require.NoError(t, svc.DB.Create(&payment).Error)

	consumer := &cashRedeemOutcomeConsumer{appsService: svc.AppsService, db: svc.DB, eventPublisher: svc.EventPublisher}
	claimed := func() bool {
		var row db.CashWalletClaim
		require.NoError(t, svc.DB.First(&row, claim.ID).Error)
		return row.ClaimedAt != nil
	}

	otherApp := hub.ID + 1000
	consumer.ConsumeEvent(context.TODO(), &events.Event{Event: "nwc_payment_failed",
		Properties: &db.Transaction{AppId: &otherApp, PaymentHash: paymentHash}}, nil)
	require.True(t, claimed(), "another app's failed payment leaves the slice alone")

	consumer.ConsumeEvent(context.TODO(), &events.Event{Event: "nwc_payment_failed", Properties: &payment}, nil)
	require.False(t, claimed(), "the slice's own failed payment releases it")
}

func newConsumerTestCashWallet(t *testing.T, svc *tests.TestService) *db.App {
	t.Helper()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", 1, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	return wallet
}

// A redeem payout whose outcome was unknown when cash_redeem answered, and that
// settles later (live, or found settled by the startup reconcile), must end the
// bill the same way an immediate settle does: its payout facts recorded on the
// slice, and the drained bill archived and deleted (NIP-CASH §Lifecycle and
// Deletion: one auto-delete policy for every drain).
func TestCashRedeemOutcomeConsumer_LateSettle_RecordsPayoutAndDeletesDrainedBill(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	wallet := newConsumerTestCashWallet(t, svc)
	tests.FundApp(svc, wallet.ID, 1000, tests.RandomHex32())

	const paymentHash = "8c4859ba70ed96328bec21b6c2f97d5453dc8c88bc56533209711701a8ff4211"
	claimedAt := time.Now()
	claim := db.CashWalletClaim{
		WalletAppID: wallet.ID, IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(),
		AmountMloki: 1000, ClaimedAt: &claimedAt, PaymentHash: paymentHash,
	}
	require.NoError(t, svc.DB.Create(&claim).Error)

	preimage := "preimage-late"
	quotedFee := uint64(10)
	settledAt := time.Now()
	payout := db.Transaction{
		AppId: &wallet.ID, Type: constants.TRANSACTION_TYPE_OUTGOING, State: constants.TRANSACTION_STATE_SETTLED,
		PaymentHash: paymentHash, AmountMloki: 1000, Preimage: &preimage, SettledAt: &settledAt,
		CashRedeemFeeMloki: &quotedFee, Metadata: []byte(`{"cash_claim_slice":true}`),
	}
	require.NoError(t, svc.DB.Create(&payout).Error)

	consumer := &cashRedeemOutcomeConsumer{appsService: svc.AppsService, db: svc.DB, eventPublisher: svc.EventPublisher}
	consumer.ConsumeEvent(context.TODO(), &events.Event{Event: "nwc_payment_sent", Properties: &payout}, nil)

	var live int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Count(&live).Error)
	require.Zero(t, live, "the drained bill is deleted, so requests to it get silence")

	var slices []db.CashBillSliceArchive
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", wallet.ID).Find(&slices).Error)
	require.Len(t, slices, 1)
	require.Equal(t, db.CashSliceStatusRedeemed, slices[0].Outcome)
	require.Equal(t, preimage, slices[0].Preimage, "the proof of payment survives into the archive")
	require.EqualValues(t, quotedFee, slices[0].RedeemFeeMloki)
}

// Only a cash_redeem payout ends a bill this way: any other payment that leaves
// a cash_wallet must not trigger the redeem bookkeeping.
func TestCashRedeemOutcomeConsumer_SettleOfOtherPayment_LeavesBillAlone(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	wallet := newConsumerTestCashWallet(t, svc)

	preimage := "preimage-other"
	payment := db.Transaction{
		AppId: &wallet.ID, Type: constants.TRANSACTION_TYPE_OUTGOING, State: constants.TRANSACTION_STATE_SETTLED,
		PaymentHash: tests.RandomHex32(), Preimage: &preimage,
	}
	consumer := &cashRedeemOutcomeConsumer{appsService: svc.AppsService, db: svc.DB, eventPublisher: svc.EventPublisher}
	consumer.ConsumeEvent(context.TODO(), &events.Event{Event: "nwc_payment_sent", Properties: &payment}, nil)

	var live int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Count(&live).Error)
	require.EqualValues(t, 1, live)
}
