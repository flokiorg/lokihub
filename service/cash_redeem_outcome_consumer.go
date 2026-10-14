package service

import (
	"context"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/transactions"
)

// cashRedeemOutcomeConsumer finishes a cash_redeem whose payment outcome was
// learned only after cash_redeem had answered: when the outcome was unknown,
// the controller kept the slice claimed rather than risk paying it twice.
//
//   - Failed: this is the only place that claim is let go.
//   - Settled: the slice's payout facts are recorded, and a bill the payout
//     drained is auto-deleted like every other drain (NIP-CASH §Lifecycle and
//     Deletion). This covers both the node's live payment subscription and the
//     startup reconcile, which publish the same event.
//
// A redeem that settles while cash_redeem is still waiting fires the settled
// event too. Both steps are safe to repeat, so whichever of the two runs first
// does the work.
type cashRedeemOutcomeConsumer struct {
	events.EventSubscriber
	appsService    apps.AppsService
	db             *gorm.DB
	eventPublisher events.EventPublisher
}

func (c *cashRedeemOutcomeConsumer) ConsumeEvent(ctx context.Context, event *events.Event, globalProperties map[string]interface{}) {
	if event.Event != "nwc_payment_failed" && event.Event != "nwc_payment_sent" {
		return
	}
	transaction, ok := event.Properties.(*db.Transaction)
	if !ok || transaction.AppId == nil {
		return
	}
	if event.Event == "nwc_payment_sent" {
		c.settled(transaction)
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

func (c *cashRedeemOutcomeConsumer) settled(transaction *db.Transaction) {
	if !transactions.IsCashRedeemPayout(transaction) || transaction.Preimage == nil {
		return
	}
	var redeemFeeMloki int64
	if transaction.CashRedeemFeeMloki != nil {
		redeemFeeMloki = int64(*transaction.CashRedeemFeeMloki) //nolint:gosec // a quoted ppm cut of a positive amount
	}

	appID := *transaction.AppId
	if err := c.appsService.RecordCashSliceRedeemSettlement(appID, apps.CashSliceRedeemPayment{
		PaymentHash:     transaction.PaymentHash,
		Preimage:        *transaction.Preimage,
		RedeemFeeMloki:  redeemFeeMloki,
		RoutingFeeMloki: int64(transaction.FeeMloki), //nolint:gosec // a routing fee the node reports, far below int64's range
		SettledAt:       transaction.SettledAt,
	}); err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", appID).Str("payment_hash", transaction.PaymentHash).
			Msg("Failed to record a settled cash_redeem payout on its slice")
	}

	deleted, err := cashwallet.AutoDeleteDrained(c.db, c.eventPublisher, appID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", appID).
			Msg("Failed to archive and auto-delete a Cash wallet drained by a settled redeem")
		return
	}
	if deleted {
		logger.Logger.Info().Uint("app_id", appID).
			Msg("Archived and auto-deleted a Cash wallet drained by a settled redeem")
	}
}
