package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/flokiorg/lokihub/hubclock"

	"github.com/nbd-wtf/go-nostr"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/circlewallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
)

// HandleDeleteCircleWalletEvent serves delete_circle_wallet (NIP-CW §Deleting a
// Circle Wallet) on the wallet's own connection: refused RESTRICTED while the
// wallet holds more than dust or has a payment to or from it in flight;
// otherwise its open invoices are cancelled at the node first (a failure
// answers INTERNAL and deletes nothing), then one transaction re-checks, moves
// the dust to the Hub and deletes the wallet. The result is {}.
func (controller *nip47Controller) HandleDeleteCircleWalletEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc) {
	method := nip47Request.Method
	refuse := func(code, message string) { respondError(publishResponse, method, code, message) }

	if app.Kind != db.AppKindCircleWallet {
		refuse(constants.ERROR_RESTRICTED, "delete_circle_wallet is served on a circle wallet's own connection only")
		return
	}
	// Its params are {}.
	if len(nip47Request.Params) > 0 && string(nip47Request.Params) != "null" {
		var params map[string]json.RawMessage
		if err := json.Unmarshal(nip47Request.Params, &params); err != nil {
			refuse(constants.ERROR_BAD_REQUEST, "delete_circle_wallet's params are an empty object")
			return
		}
	}

	inFlight, err := circlelife.InFlight(controller.db, app.ID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to read a circle wallet's payments")
		refuse(constants.ERROR_INTERNAL, "could not read this wallet's payments")
		return
	}
	if inFlight || queries.GetIsolatedBalance(controller.db, app.ID) > circlelife.Dust(app) {
		refuse(constants.ERROR_RESTRICTED, "this wallet holds more than dust or has a payment in flight; pay it out first")
		return
	}

	// Its open invoices go first, at the node, so none can be paid once the
	// wallet is gone.
	if err := controller.transactionsService.CancelOpenCircleInvoices(ctx, app.ID, controller.lnClient); err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to cancel a deleted circle wallet's invoices")
		refuse(constants.ERROR_INTERNAL, "could not cancel this wallet's open invoices; nothing was deleted, please retry")
		return
	}

	var openHolds []string
	err = controller.db.Transaction(func(tx *gorm.DB) error {
		var current db.App
		if err := tx.Where("id = ?", app.ID).First(&current).Error; err != nil {
			return err
		}
		// This request outlives its wallet: its reply is recorded against it
		// once the wallet is gone.
		if err := tx.Model(&db.RequestEvent{}).Where("id = ?", requestEventId).Update("app_id", nil).Error; err != nil {
			return err
		}
		var err error
		openHolds, err = circlewallet.DeleteWallet(tx, &current, hubclock.Now())
		return err
	})
	switch {
	case errors.Is(err, circlewallet.ErrNotDeletable):
		refuse(constants.ERROR_RESTRICTED, "this wallet holds more than dust or has a payment in flight; pay it out first")
		return
	case err != nil:
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to delete a circle wallet")
		refuse(constants.ERROR_INTERNAL, "could not delete this wallet, please retry")
		return
	}

	controller.transactionsService.CancelHoldInvoicesAtNode(ctx, openHolds, controller.lnClient)
	controller.eventPublisher.Publish(&events.Event{
		Event:      "nwc_app_deleted",
		Properties: map[string]interface{}{"id": app.ID},
	})
	logger.Logger.Info().Uint("app_id", app.ID).Msg("A member deleted its circle wallet")
	publishResponse(&models.Response{ResultType: method, Result: struct{}{}}, nostr.Tags{})
}
