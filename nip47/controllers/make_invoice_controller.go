package controllers

import (
	"context"

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

	expiry := makeInvoiceParams.Expiry

	transaction, err := controller.transactionsService.MakeInvoice(ctx, makeInvoiceParams.Amount, makeInvoiceParams.Description, makeInvoiceParams.DescriptionHash, expiry, makeInvoiceParams.Metadata, controller.lnClient, &appId, &requestEventId, nil, nil, nil, nil, nil)
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
