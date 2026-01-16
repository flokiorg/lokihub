package controllers

import (
	"context"

	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
)

type cancelHoldInvoiceParams struct {
	PaymentHash string `json:"payment_hash"`
}
type cancelHoldInvoiceResponse struct{}

func (controller *nip47Controller) HandleCancelHoldInvoiceEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, appId uint, publishResponse func(*models.Response, nostr.Tags)) {
	cancelHoldInvoiceParams := &cancelHoldInvoiceParams{}
	decodeErrResp := decodeRequest(nip47Request, cancelHoldInvoiceParams)
	if decodeErrResp != nil {
		publishResponse(decodeErrResp, nostr.Tags{})
		return
	}

	logger.Logger.Info().
		Interface("requestEventId", requestEventId).
		Interface("appId", appId).
		Interface("paymentHash", cancelHoldInvoiceParams.PaymentHash).
		Msg("Canceling hold invoice")

	err := controller.transactionsService.CancelHoldInvoice(ctx, cancelHoldInvoiceParams.PaymentHash, controller.lnClient)
	if err != nil {
		logger.Logger.Error().Err(err).
			Interface("request_event_id", requestEventId).
			Interface("appId", appId).
			Interface("paymentHash", cancelHoldInvoiceParams.PaymentHash).
			Msg("Failed to cancel hold invoice")

		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Error:      mapNip47Error(err),
		}, nostr.Tags{})
		return
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result:     &cancelHoldInvoiceResponse{},
	}, nostr.Tags{})
}
