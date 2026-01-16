package controllers

import (
	"context"

	"github.com/flokiorg/lokihub/db/queries"
	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
)

type getBudgetResponse struct {
	UsedBudget    uint64  `json:"used_budget"`
	TotalBudget   uint64  `json:"total_budget"`
	RenewsAt      *uint64 `json:"renews_at,omitempty"`
	RenewalPeriod string  `json:"renewal_period"`
}

func (controller *nip47Controller) HandleGetBudgetEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc) {

	logger.Logger.Debug().
			Interface("request_event_id", requestEventId).
			Msg("Getting budget")

	appPermission := db.AppPermission{}
	controller.db.Where("app_id = ? AND scope = ?", app.ID, models.PAY_INVOICE_METHOD).First(&appPermission)

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
		TotalBudget:   uint64(maxAmount * 1000),
		UsedBudget:    usedBudget * 1000,
		RenewalPeriod: appPermission.BudgetRenewal,
		RenewsAt:      queries.GetBudgetRenewsAt(appPermission.BudgetRenewal),
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result:     responsePayload,
	}, nostr.Tags{})
}
