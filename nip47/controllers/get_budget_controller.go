package controllers

import (
	"context"
	"time"

	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
)

type getBudgetResponse struct {
	UsedBudget    uint64  `json:"used_budget"`
	TotalBudget   uint64  `json:"total_budget"`
	RenewsAt      *uint64 `json:"renews_at,omitempty"`
	RenewalPeriod string  `json:"renewal_period"`
	// TotalReceiveBudget and UsedReceiveBudget are a Circle Wallet's receive
	// cap and what counts against it this period (NIP-CW §What a Circle Wallet
	// Connection Allows).
	TotalReceiveBudget *uint64 `json:"total_receive_budget,omitempty"`
	UsedReceiveBudget  *uint64 `json:"used_receive_budget,omitempty"`
}

// circleBudget is a Circle Wallet's get_budget: both caps for the current
// period, each used figure clamped so the remaining room is never below zero.
func (controller *nip47Controller) circleBudget(app *db.App, now time.Time) (*getBudgetResponse, error) {
	caps, err := circlemoney.WalletCaps(controller.db, app)
	if err != nil {
		return nil, err
	}
	start, next, err := circlemoney.Period(circlemoney.Anchor(app), caps.Renewal, now)
	if err != nil {
		return nil, err
	}
	usedSend := min(circlemoney.SendUsage(controller.db, app.ID, start.Unix()), caps.MaxAmount)
	usedReceive, err := circlemoney.ReceiveUsage(controller.db, app, caps, now)
	if err != nil {
		return nil, err
	}
	usedReceive = min(usedReceive, caps.MaxReceive)
	totalReceive, usedReceiveU := uint64(max(caps.MaxReceive, 0)), uint64(max(usedReceive, 0))
	response := &getBudgetResponse{
		TotalBudget:        uint64(max(caps.MaxAmount, 0)),
		UsedBudget:         uint64(max(usedSend, 0)),
		RenewalPeriod:      caps.Renewal,
		TotalReceiveBudget: &totalReceive,
		UsedReceiveBudget:  &usedReceiveU,
	}
	if !next.IsZero() {
		renews := uint64(next.Unix()) //nolint:gosec // a future unix time
		response.RenewsAt = &renews
	}
	return response, nil
}

func (controller *nip47Controller) HandleGetBudgetEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc) {

	logger.Logger.Debug().
		Interface("request_event_id", requestEventId).
		Msg("Getting budget")

	if circlemoney.IsCircleWallet(app) {
		budget, err := controller.circleBudget(app, time.Now())
		if err != nil {
			logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to read a circle wallet's budget")
			publishResponse(&models.Response{ResultType: nip47Request.Method,
				Error: &models.Error{Code: constants.ERROR_INTERNAL, Message: "could not read this wallet's budget"}}, nostr.Tags{})
			return
		}
		publishResponse(&models.Response{ResultType: nip47Request.Method, Result: budget}, nostr.Tags{})
		return
	}

	// Matches against constants.PayCapableScopes, not just pay_invoice alone —
	// a cash_wallet's budget-bearing scope is cash_redeem instead (though
	// cash_wallet never actually reaches this handler in practice: get_budget
	// is carved out of the system-wide always-granted list for that app kind
	// in event_handler.go). Kept general for any future scope in the same
	// position.
	appPermission := db.AppPermission{}
	controller.db.Where("app_id = ? AND scope IN ?", app.ID, constants.PayCapableScopes).First(&appPermission)

	maxAmount := appPermission.MaxAmountLoki
	if maxAmount == 0 {
		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Result:     struct{}{},
		}, nostr.Tags{})
		return
	}

	usedBudget := queries.GetBudgetUsageSat(controller.db, &appPermission)
	responsePayload := &getBudgetResponse{
		TotalBudget:   uint64(maxAmount * 1000), //nolint:gosec // app-internal budget value, always non-negative
		UsedBudget:    usedBudget * 1000,
		RenewalPeriod: appPermission.BudgetRenewal,
		RenewsAt:      queries.GetBudgetRenewsAt(appPermission.BudgetRenewal),
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result:     responsePayload,
	}, nostr.Tags{})
}
