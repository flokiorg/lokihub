package controllers

import (
	"context"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/transactions"
	"github.com/nbd-wtf/go-nostr"
)

type recipientStatus struct {
	IdentityType  string `json:"identity_type"`
	IdentityValue string `json:"identity_value"`
	AmountMloki   int64  `json:"amount_mloki"`
	Claimed       bool   `json:"claimed"`
	ClaimedAt     *int64 `json:"claimed_at,omitempty"`
	// RedeemFeeMloki/NetRedeemableMloki are this slice's cash_redeem quote —
	// the fee CalculateFeeSkimMloki(AmountMloki, this claim's own
	// RedeemFeePpm) resolves to, and what's left after it. This is
	// necessarily the WORST-CASE (external) quote: list_recipients has no
	// invoice to check in advance whether a given redemption will actually
	// resolve to a same-node payment, which stays fee-free regardless (see
	// cash_redeem_controller.go). A redemption may end up paying out more
	// than this NetRedeemableMloki figure (the full AmountMloki, if
	// same-node); it will never pay out less.
	RedeemFeeMloki     int64 `json:"redeem_fee_mloki"`
	NetRedeemableMloki int64 `json:"net_redeemable_mloki"`
}

type listRecipientsResponse struct {
	Recipients []recipientStatus `json:"recipients"`
}

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

	recipients := make([]recipientStatus, len(claims))
	for i, c := range claims {
		redeemFeeMloki := transactions.CalculateFeeSkimMloki(uint64(c.AmountMloki), c.RedeemFeePpm) //nolint:gosec // AmountMloki is always non-negative
		status := recipientStatus{
			IdentityType:       c.IdentityType,
			IdentityValue:      c.IdentityValue,
			AmountMloki:        c.AmountMloki,
			Claimed:            c.ClaimedAt != nil,
			RedeemFeeMloki:     int64(redeemFeeMloki),                 //nolint:gosec // a <=100% cut of an int64 amount, always fits
			NetRedeemableMloki: c.AmountMloki - int64(redeemFeeMloki), //nolint:gosec // redeemFeeMloki <= AmountMloki by construction
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
