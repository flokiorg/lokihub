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

	consumer := &cashRedeemFailureConsumer{appsService: svc.AppsService}
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
