package controllers

import (
	"context"

	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
)

type settleHoldInvoiceParams struct {
	Preimage string `json:"preimage"`
}
type settleHoldInvoiceResponse struct{}

func (controller *nip47Controller) HandleSettleHoldInvoiceEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, appId uint, publishResponse func(*models.Response, nostr.Tags)) {
	settleHoldInvoiceParams := &settleHoldInvoiceParams{}
	decodeErrResp := decodeRequest(nip47Request, settleHoldInvoiceParams)
	if decodeErrResp != nil {
		publishResponse(decodeErrResp, nostr.Tags{})
		return
	}

	logger.Logger.Info().
		Interface("requestEventId", requestEventId).
		Interface("appId", appId).
		Interface("preimage", settleHoldInvoiceParams.Preimage).
		Msg("Settling hold invoice")

	_, err := controller.transactionsService.SettleHoldInvoice(ctx, settleHoldInvoiceParams.Preimage, controller.lnClient)
	if err != nil {
		logger.Logger.Error().Err(err).
			Interface("request_event_id", requestEventId).
			Interface("appId", appId).
			Interface("preimage", settleHoldInvoiceParams.Preimage).
			Msg("Failed to settle hold invoice")

		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Error:      mapNip47Error(err),
		}, nostr.Tags{})
		return
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result:     &settleHoldInvoiceResponse{},
	}, nostr.Tags{})
}
