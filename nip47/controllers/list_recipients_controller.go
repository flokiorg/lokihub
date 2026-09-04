package controllers

import (
	"context"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/transactions"
)

// list_recipients' response is github.com/ohstr/nmilat/nipcash's own exported
// ListRecipientsResult/RecipientStatus — same wire shape, adopted directly
// instead of maintaining a parallel copy (nmilat migration, PR #90). Two
// accepted, tested differences from this controller's former local types:
//   - RecipientStatus's numeric fields (AmountMillis, RedeemFeeMillis,
//     NetRedeemableMillis, MinTransferMillis) are uint64, not int64 - every
//     value here is a Mloki amount or fee derived from one, always
//     non-negative by construction (same invariant the former int64 fields'
//     own //nolint:gosec comments already documented).
//   - RecipientStatus.IdentityValue carries `omitempty`, the former local
//     type's didn't - a bearer slice's identity_value (always "") is now
//     omitted from the response entirely rather than sent as an empty
//     string. No existing test asserts the raw JSON shape of this field, and
//     any reasonable JSON consumer treats an omitted optional field and an
//     empty one identically.
//
// HandleListRecipientsEvent returns the full roster of a shared cash_wallet's
// recipients — identity, entitled amount, and claimed status only. This is
// deliberately a transparent, shared-view method (any holder of the
// connection sees every recipient's row, not just their own) rather than a
// caller-scoped one, matching the model already accepted for get_balance —
// but it never includes invoice/preimage/payment detail, since a cash_wallet
// carries no list_transactions grant at all.
func (controller *nip47Controller) HandleListRecipientsEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc) {
	if app.Kind != db.AppKindCashWallet {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "list_recipients requires a cash_wallet app")
		return
	}

	claims, err := controller.appsService.ListClaimsForWallet(app.ID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to list Cash wallet recipients")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to list recipients")
		return
	}

	var expiresAt *int64
	if app.ExpiresAt != nil {
		ts := app.ExpiresAt.Unix()
		expiresAt = &ts
	}

	recipients := make([]nipcash.RecipientStatus, len(claims))
	for i, c := range claims {
		redeemFeeMloki := transactions.CalculateFeeSkimMloki(uint64(c.AmountMloki), c.RedeemFeePpm) //nolint:gosec // AmountMloki is always non-negative
		status := nipcash.RecipientStatus{
			IdentityType:        c.IdentityType,
			IdentityValue:       c.IdentityValue,
			AmountMillis:        uint64(c.AmountMloki), //nolint:gosec // AmountMloki is always non-negative
			Claimed:             c.ClaimedAt != nil,
			RedeemFeeMillis:     redeemFeeMloki,
			NetRedeemableMillis: uint64(c.AmountMloki) - redeemFeeMloki, //nolint:gosec // redeemFeeMloki <= AmountMloki by construction
			MinTransferMillis:   uint64(c.MinTransferMloki),             //nolint:gosec // MinTransferMloki is always non-negative
			ExpiresAt:           expiresAt,
		}
		if c.ClaimedAt != nil {
			claimedAt := c.ClaimedAt.Unix()
			status.ClaimedAt = &claimedAt
		}
		recipients[i] = status
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result:     nipcash.ListRecipientsResult{Recipients: recipients},
	}, nostr.Tags{})
}
