package controllers

import (
	"context"
	"fmt"
	"strings"

	decodepay "github.com/flokiorg/flndecodepay"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
)

type lookupInvoiceParams struct {
	Invoice     string `json:"invoice"`
	PaymentHash string `json:"payment_hash"`
}

type lookupInvoiceResponse struct {
	models.Transaction
}

func (controller *nip47Controller) HandleLookupInvoiceEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, appId uint, publishResponse publishFunc) {

	lookupInvoiceParams := &lookupInvoiceParams{}
	resp := decodeRequest(nip47Request, lookupInvoiceParams)
	if resp != nil {
		publishResponse(resp, nostr.Tags{})
		return
	}

	logger.Logger.Info().
		Interface("invoice", lookupInvoiceParams.Invoice).
		Interface("payment_hash", lookupInvoiceParams.PaymentHash).
		Interface("request_event_id", requestEventId).
		Msg("Looking up invoice")

	paymentHash := lookupInvoiceParams.PaymentHash

	if paymentHash == "" {
		paymentRequest, err := decodepay.Decodepay(strings.ToLower(lookupInvoiceParams.Invoice))
		if err != nil {
			logger.Logger.Error().Err(err).
				Interface("request_event_id", requestEventId).
				Interface("invoice", lookupInvoiceParams.Invoice).
				Msg("Failed to decode bolt11 invoice")

			publishResponse(&models.Response{
				ResultType: nip47Request.Method,
				Error: &models.Error{
					Code:    constants.ERROR_BAD_REQUEST,
					Message: fmt.Sprintf("Failed to decode bolt11 invoice: %s", err.Error()),
				},
			}, nostr.Tags{})
			return
		}
		paymentHash = paymentRequest.PaymentHash
	}

	dbTransaction, err := controller.transactionsService.LookupTransaction(ctx, paymentHash, nil, controller.lnClient, &appId)
	if err != nil {
		logger.Logger.Info().Err(err).
			Interface("request_event_id", requestEventId).
			Interface("invoice", lookupInvoiceParams.Invoice).
			Interface("payment_hash", paymentHash).
			Msg("Failed to lookup invoice")

		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Error:      mapNip47Error(err),
		}, nostr.Tags{})
		return
	}

	responsePayload := &lookupInvoiceResponse{
		Transaction: *models.ToNip47Transaction(dbTransaction),
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result:     responsePayload,
	}, nostr.Tags{})
}
