package controllers

import (
	"context"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
)

type recipientStatus struct {
	IdentityType  string `json:"identity_type"`
	IdentityValue string `json:"identity_value"`
	AmountMloki   int64  `json:"amount_mloki"`
	Claimed       bool   `json:"claimed"`
	ClaimedAt     *int64 `json:"claimed_at,omitempty"`
}

type listRecipientsResponse struct {
	Recipients []recipientStatus `json:"recipients"`
}

// HandleListRecipientsEvent returns the full roster of a shared jit_wallet's
// recipients — identity, entitled amount, and claimed status only. This is
// deliberately a transparent, shared-view method (any holder of the
// connection sees every recipient's row, not just their own) rather than a
// caller-scoped one, matching the model already accepted for get_balance —
// but it never includes invoice/preimage/payment detail, since a jit_wallet
// carries no list_transactions grant at all.
func (controller *nip47Controller) HandleListRecipientsEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc) {
	if app.Kind != db.AppKindJITWallet {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "list_recipients requires a jit_wallet app")
		return
	}

	claims, err := controller.appsService.ListClaimsForWallet(app.ID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to list JIT wallet recipients")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to list recipients")
		return
	}

	recipients := make([]recipientStatus, len(claims))
	for i, c := range claims {
		status := recipientStatus{
			IdentityType:  c.IdentityType,
			IdentityValue: c.IdentityValue,
			AmountMloki:   c.AmountMloki,
			Claimed:       c.ClaimedAt != nil,
		}
		if c.ClaimedAt != nil {
			claimedAt := c.ClaimedAt.Unix()
			status.ClaimedAt = &claimedAt
		}
		recipients[i] = status
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result:     listRecipientsResponse{Recipients: recipients},
	}, nostr.Tags{})
}
