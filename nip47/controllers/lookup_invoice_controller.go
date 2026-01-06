package controllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokiorg/lokihub/constants"
	decodepay "github.com/flokiorg/lokihub/lndecodepay"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
	"github.com/sirupsen/logrus"
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

	logger.Logger.WithFields(logrus.Fields{
		"invoice":          lookupInvoiceParams.Invoice,
		"payment_hash":     lookupInvoiceParams.PaymentHash,
		"request_event_id": requestEventId,
	}).Info("Looking up invoice")

	paymentHash := lookupInvoiceParams.PaymentHash

	if paymentHash == "" {
		paymentRequest, err := decodepay.Decodepay(strings.ToLower(lookupInvoiceParams.Invoice))
		if err != nil {
			logger.Logger.WithFields(logrus.Fields{
				"request_event_id": requestEventId,
				"invoice":          lookupInvoiceParams.Invoice,
			}).WithError(err).Error("Failed to decode bolt11 invoice")

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
		logger.Logger.WithFields(logrus.Fields{
			"request_event_id": requestEventId,
			"invoice":          lookupInvoiceParams.Invoice,
			"payment_hash":     paymentHash,
		}).Infof("Failed to lookup invoice: %v", err)

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
