package controllers

import (
	"context"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
)

type makeInvoiceParams struct {
	Amount          uint64                 `json:"amount"`
	Description     string                 `json:"description"`
	DescriptionHash string                 `json:"description_hash"`
	Expiry          uint64                 `json:"expiry"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}
type makeInvoiceResponse struct {
	models.Transaction
}

func (controller *nip47Controller) HandleMakeInvoiceEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, appId uint, publishResponse publishFunc) {

	makeInvoiceParams := &makeInvoiceParams{}
	resp := decodeRequest(nip47Request, makeInvoiceParams)
	if resp != nil {
		publishResponse(resp, nostr.Tags{})
		return
	}

	logger.Logger.Debug().
		Interface("app_id", appId).
		Interface("request_event_id", requestEventId).
		Interface("amount", makeInvoiceParams.Amount).
		Interface("description", makeInvoiceParams.Description).
		Interface("description_hash", makeInvoiceParams.DescriptionHash).
		Interface("expiry", makeInvoiceParams.Expiry).
		Interface("metadata", makeInvoiceParams.Metadata).
		Msg("Handling make_invoice request")

	// Circle child wallets cannot accumulate above their MaxAmountLoki cap.
	// The per-app lock (activeCircleInvoices) covers both the balance read and the
	// MakeInvoice call below, preventing two concurrent requests from both passing
	// the cap check on the same stale balance (TOCTOU race).
	var circleApp db.App
	if controller.db.Where("id = ? AND parent_kind = ?", appId, db.ParentKindCircle).Limit(1).Find(&circleApp).RowsAffected > 0 {
		var maxMloki int64
		controller.db.Model(&db.AppPermission{}).
			Where("app_id = ? AND scope = ?", appId, constants.MAKE_INVOICE_SCOPE).
			Pluck("max_amount_loki * 1000", &maxMloki)
		if maxMloki > 0 {
			if _, loaded := controller.activeCircleInvoices.LoadOrStore(appId, struct{}{}); loaded {
				publishResponse(&models.Response{
					ResultType: nip47Request.Method,
					Error: &models.Error{
						Code:    constants.ERROR_RATE_LIMITED,
						Message: "concurrent invoice creation in progress",
					},
				}, nostr.Tags{})
				return
			}
			defer controller.activeCircleInvoices.Delete(appId)

			balance := queries.GetIsolatedBalance(controller.db, appId)
			if balance+int64(makeInvoiceParams.Amount) > maxMloki {
				publishResponse(&models.Response{
					ResultType: nip47Request.Method,
					Error: &models.Error{
						Code:    constants.ERROR_QUOTA_EXCEEDED,
						Message: "invoice amount would exceed circle wallet cap",
					},
				}, nostr.Tags{})
				return
			}
		}
	}

	expiry := makeInvoiceParams.Expiry

	transaction, err := controller.transactionsService.MakeInvoice(ctx, makeInvoiceParams.Amount, makeInvoiceParams.Description, makeInvoiceParams.DescriptionHash, expiry, makeInvoiceParams.Metadata, controller.lnClient, &appId, &requestEventId, nil, nil, nil, nil, nil, nil)
	if err != nil {
		logger.Logger.Info().Err(err).
			Interface("request_event_id", requestEventId).
			Interface("amount", makeInvoiceParams.Amount).
			Interface("description", makeInvoiceParams.Description).
			Interface("descriptionHash", makeInvoiceParams.DescriptionHash).
			Interface("expiry", makeInvoiceParams.Expiry).
			Msg("Failed to make invoice")

		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Error:      mapNip47Error(err),
		}, nostr.Tags{})
		return
	}

	nip47Transaction := models.ToNip47Transaction(transaction)
	responsePayload := &makeInvoiceResponse{
		Transaction: *nip47Transaction,
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result:     responsePayload,
	}, nostr.Tags{})
}
