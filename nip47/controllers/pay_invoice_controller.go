package controllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	decodepay "github.com/flokiorg/lokihub/decodepay"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
)

type payInvoiceParams struct {
	Invoice  string                 `json:"invoice"`
	Amount   *uint64                `json:"amount"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

func (controller *nip47Controller) HandlePayInvoiceEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, tags nostr.Tags) {
	payParams := &payInvoiceParams{}
	resp := decodeRequest(nip47Request, payParams)
	if resp != nil {
		publishResponse(resp, tags)
		return
	}

	bolt11 := payParams.Invoice
	// Convert invoice to lowercase string
	bolt11 = strings.ToLower(bolt11)
	paymentRequest, err := decodepay.Decode(bolt11)
	if err != nil {
		logger.Logger.Error().Err(err).
			Interface("request_event_id", requestEventId).
			Interface("app_id", app.ID).
			Interface("bolt11", bolt11).
			Msg("Failed to decode bolt11 invoice")

		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Error: &models.Error{
				Code:    constants.ERROR_BAD_REQUEST,
				Message: fmt.Sprintf("Failed to decode bolt11 invoice: %s", err.Error()),
			},
		}, tags)
		return
	}

	// JIT full-drain enforcement is applied inside SendPaymentSync (transactions_service.go)
	// so it covers all payment paths (NIP-47, HTTP API, keysend) uniformly.
	controller.pay(bolt11, payParams.Amount, payParams.Metadata, paymentRequest, nip47Request, requestEventId, app, publishResponse, tags)
}

func (controller *nip47Controller) pay(bolt11 string, amount *uint64, metadata map[string]interface{}, paymentRequest *decodepay.Bolt11, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, tags nostr.Tags) {
	logger.Logger.Info().
		Interface("request_event_id", requestEventId).
		Interface("app_id", app.ID).
		Interface("bolt11", bolt11).
		Msg("Sending payment")

	// Prevent user-supplied metadata from spoofing internal_transfer (bypasses
	// JIT full-drain enforcement) or jit_claim_slice (bypasses the fee-reserve
	// headroom in validateCanPay's balance/budget checks) — both flags are
	// meant to be set only by their own trusted call sites (hub cleanup/self
	// -payment, and claim_funds_controller.go's own proof-gated payout,
	// respectively), never by an arbitrary pay_invoice/multi_pay_invoice caller.
	if metadata != nil {
		delete(metadata, "internal_transfer")
		delete(metadata, "jit_claim_slice")
	}

	transaction, err := controller.transactionsService.SendPaymentSync(bolt11, amount, metadata, controller.lnClient, &app.ID, &requestEventId)
	if err != nil {
		logger.Logger.Error().Err(err).
			Interface("request_event_id", requestEventId).
			Interface("app_id", app.ID).
			Interface("bolt11", bolt11).
			Msg("Failed to send payment")
		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Error:      mapNip47Error(err),
		}, tags)
		return
	}

	if transaction == nil || transaction.Preimage == nil {
		logger.Logger.Error().
			Interface("request_event_id", requestEventId).
			Interface("app_id", app.ID).
			Msg("Payment succeeded but transaction or preimage is nil")
		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Error: &models.Error{
				Code:    constants.ERROR_INTERNAL,
				Message: "payment completed but preimage unavailable",
			},
		}, tags)
		return
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result: payResponse{
			Preimage: *transaction.Preimage,
			FeesPaid: transaction.FeeMloki,
		},
	}, tags)
}
