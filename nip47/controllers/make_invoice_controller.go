package controllers

import (
	"context"
	"math"
	"sync"
	"time"

	"gorm.io/gorm"

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

// circleInvoiceGuard holds an entry per circle wallet while a make_invoice for it
// checks the cap and issues the invoice.
var circleInvoiceGuard sync.Map // map[uint]struct{}

// outstandingIncomingMloki sums appID's incoming invoices that can still be paid.
// Expiry is compared in Go: the sqlite driver stores times in a form SQL date
// functions can't read.
func outstandingIncomingMloki(tx *gorm.DB, appID uint, now time.Time) int64 {
	var pending []db.Transaction
	tx.Model(&db.Transaction{}).
		Select("amount_mloki", "expires_at").
		Where("app_id = ? AND type = ? AND state IN ?", appID, constants.TRANSACTION_TYPE_INCOMING,
			[]string{constants.TRANSACTION_STATE_PENDING, constants.TRANSACTION_STATE_ACCEPTED}).
		Find(&pending)
	var sum int64
	for _, t := range pending {
		if t.ExpiresAt == nil || t.ExpiresAt.After(now) {
			sum += int64(t.AmountMloki) //nolint:gosec // invoice amounts are far below int64 range
		}
	}
	return sum
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

	// Circle child wallets cannot accumulate above their MaxAmountLoki cap. The
	// per-app guard covers both the cap read and the MakeInvoice call below, so two
	// concurrent requests can't both pass on the same stale figure (TOCTOU race).
	// It is package-level: production builds a new controller per request, so a
	// per-controller guard never saw a second request.
	var circleApp db.App
	if controller.db.Where("id = ? AND parent_kind = ?", appId, db.ParentKindCircle).Limit(1).Find(&circleApp).RowsAffected > 0 {
		var maxMloki int64
		controller.db.Model(&db.AppPermission{}).
			Where("app_id = ? AND scope = ?", appId, constants.MAKE_INVOICE_SCOPE).
			Pluck("max_amount_loki * 1000", &maxMloki)
		if maxMloki > 0 {
			if _, loaded := circleInvoiceGuard.LoadOrStore(appId, struct{}{}); loaded {
				publishResponse(&models.Response{
					ResultType: nip47Request.Method,
					Error: &models.Error{
						Code:    constants.ERROR_RATE_LIMITED,
						Message: "concurrent invoice creation in progress",
					},
				}, nostr.Tags{})
				return
			}
			defer circleInvoiceGuard.Delete(appId)

			// Invoices already issued and still payable count too: each can still
			// bring its amount in, and settling one re-checks nothing.
			committed := queries.GetIsolatedBalance(controller.db, appId) + outstandingIncomingMloki(controller.db, appId, time.Now())
			// makeInvoiceParams.Amount is caller-suppliable; a value beyond
			// int64 range would wrap negative and bypass the cap check
			// below, so fail closed on the overflow itself.
			if makeInvoiceParams.Amount > math.MaxInt64 || committed+int64(makeInvoiceParams.Amount) > maxMloki {
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
