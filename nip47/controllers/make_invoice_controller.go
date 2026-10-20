package controllers

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/constants"
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

// circleInvoiceGuard holds an entry per circle wallet while a make_invoice for it
// checks the cap and issues the invoice.
var circleInvoiceGuard sync.Map // map[uint]struct{}

// reserveCircleReceiveCap checks that an invoice of amountMloki keeps a circle
// wallet within its MaxAmountLoki cap. Every way to receive (make_invoice,
// make_hold_invoice) goes through it. On success the caller holds the wallet's
// guard until it calls release, which covers both the cap read and issuing the
// invoice, so two concurrent requests can't both pass on the same stale figure.
// The guard is package-level: production builds a new controller per request.
// A non-nil refusal is the error to publish.
func (controller *nip47Controller) reserveCircleReceiveCap(appId uint, amountMloki uint64) (release func(), refusal *models.Error) {
	noop := func() {}
	if _, capped := queries.CircleReceiveRoomMloki(controller.db, appId, time.Now()); !capped {
		return noop, nil
	}
	// A zero-amount invoice lets the payer choose the amount, so no cap could hold.
	if amountMloki == 0 {
		return noop, &models.Error{
			Code:    constants.ERROR_BAD_REQUEST,
			Message: "a circle wallet invoice needs an amount",
		}
	}
	if _, loaded := circleInvoiceGuard.LoadOrStore(appId, struct{}{}); loaded {
		return noop, &models.Error{
			Code:    constants.ERROR_RATE_LIMITED,
			Message: "concurrent invoice creation in progress",
		}
	}
	release = func() { circleInvoiceGuard.Delete(appId) }

	// The room is read under the guard. Invoices already issued and still
	// payable count against it: each can still bring its amount in, and
	// settling one re-checks nothing. The amount is caller-suppliable; a value
	// beyond int64 range would wrap negative, so fail closed on the overflow.
	room, _ := queries.CircleReceiveRoomMloki(controller.db, appId, time.Now())
	if amountMloki > math.MaxInt64 || int64(amountMloki) > room {
		release()
		return noop, &models.Error{
			Code:    constants.ERROR_QUOTA_EXCEEDED,
			Message: "invoice amount would exceed circle wallet cap",
		}
	}
	return release, nil
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

	release, refusal := controller.reserveCircleReceiveCap(appId, makeInvoiceParams.Amount)
	if refusal != nil {
		publishResponse(&models.Response{ResultType: nip47Request.Method, Error: refusal}, nostr.Tags{})
		return
	}
	defer release()

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
