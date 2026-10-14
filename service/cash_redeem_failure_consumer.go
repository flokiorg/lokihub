package service

import (
	"context"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
)

// cashRedeemFailureConsumer reopens a cash slice whose redeem payment failed
// after cash_redeem had already answered: when the payment's outcome was
// unknown, the controller kept the slice claimed rather than risk paying it
// twice, and this is the only place that claim is let go.
type cashRedeemFailureConsumer struct {
	events.EventSubscriber
	appsService apps.AppsService
}

func (c *cashRedeemFailureConsumer) ConsumeEvent(ctx context.Context, event *events.Event, globalProperties map[string]interface{}) {
	if event.Event != "nwc_payment_failed" {
		return
	}
	transaction, ok := event.Properties.(*db.Transaction)
	if !ok || transaction.AppId == nil {
		return
	}
	released, err := c.appsService.ReleaseCashSliceForFailedPayment(*transaction.AppId, transaction.PaymentHash)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", *transaction.AppId).Str("payment_hash", transaction.PaymentHash).
			Msg("Failed to release cash slice after its redeem payment failed")
		return
	}
	if released {
		logger.Logger.Info().Uint("app_id", *transaction.AppId).Str("payment_hash", transaction.PaymentHash).
			Msg("Released cash slice after its redeem payment failed")
	}
}
