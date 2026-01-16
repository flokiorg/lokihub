package controllers

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	decodepay "github.com/flokiorg/lokihub/lndecodepay"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
)

type multiPayInvoiceElement struct {
	payInvoiceParams
	Id string `json:"id"`
}

type multiPayInvoiceParams struct {
	Invoices []multiPayInvoiceElement `json:"invoices"`
}

func (controller *nip47Controller) HandleMultiPayInvoiceEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc) {
	multiPayParams := &multiPayInvoiceParams{}
	resp := decodeRequest(nip47Request, multiPayParams)
	if resp != nil {
		publishResponse(resp, nostr.Tags{})
		return
	}
	logger.Logger.Debug().Interface("multiPayParams", multiPayParams).Msg("sending multi payment")

	var wg sync.WaitGroup
	wg.Add(len(multiPayParams.Invoices))
	for _, invoiceInfo := range multiPayParams.Invoices {
		go func(invoiceInfo multiPayInvoiceElement) {
			defer wg.Done()
			bolt11 := invoiceInfo.Invoice
			metadata := invoiceInfo.Metadata
			// Convert invoice to lowercase string
			bolt11 = strings.ToLower(bolt11)
			paymentRequest, err := decodepay.Decodepay(bolt11)
			if err != nil {
				logger.Logger.Error().Err(err).
					Interface("request_event_id", requestEventId).
					Interface("appId", app.ID).
					Interface("bolt11", bolt11).
					Msg("Failed to decode bolt11 invoice")

				// TODO: Decide what to do if id is empty
				dTag := []string{"d", invoiceInfo.Id}
				publishResponse(&models.Response{
					ResultType: nip47Request.Method,
					Error: &models.Error{
						Code:    constants.ERROR_BAD_REQUEST,
						Message: fmt.Sprintf("Failed to decode bolt11 invoice: %s", err.Error()),
					},
				}, nostr.Tags{dTag})
				return
			}

			invoiceDTagValue := invoiceInfo.Id
			if invoiceDTagValue == "" {
				invoiceDTagValue = paymentRequest.PaymentHash
			}
			dTag := []string{"d", invoiceDTagValue}

			controller.
				pay(bolt11, invoiceInfo.Amount, metadata, &paymentRequest, nip47Request, requestEventId, app, publishResponse, nostr.Tags{dTag})
		}(invoiceInfo)
	}

	wg.Wait()
}
