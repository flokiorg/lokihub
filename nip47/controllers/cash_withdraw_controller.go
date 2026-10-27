package controllers

import (
	"context"
	"errors"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/cashwithdraw"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
)

// WithdrawController serves the two on-chain withdrawal methods (NIP-CASH
// §Redeeming On-chain); the private dispatch calls it.
type WithdrawController interface {
	HandleCashWithdrawQuoteEvent(ctx context.Context, nip47Request *models.Request, requestEventID uint, app *db.App,
		publishResponse publishFunc, caller cashwithdraw.Caller)
	HandleCashWithdrawEvent(ctx context.Context, nip47Request *models.Request, requestEventID uint, app *db.App,
		publishResponse publishFunc, call *PrivateCall, caller cashwithdraw.Caller, meta cashwithdraw.CallMeta)
}

// WithWithdraw sets the on-chain withdrawal service.
func (controller *nip47Controller) WithWithdraw(w *cashwithdraw.Service) *nip47Controller {
	controller.withdraw = w
	return controller
}

// respondWithdrawError answers a withdrawal refusal with its code and, for the
// spec's table rows, its detail; anything else is INTERNAL, never echoing an
// internal error.
func respondWithdrawError(publishResponse publishFunc, method string, app *db.App, err error, fallback string) {
	var we *nipcash.WithdrawError
	var r *cashwithdraw.Refusal
	switch {
	case errors.As(err, &we):
		message := we.Reason
		if message == "" {
			message = we.Code
		}
		publishResponse(&models.Response{ResultType: method,
			Error: &models.Error{Code: we.Code, Message: message, Detail: string(nipcash.ParseDetail(string(we.Detail)))}}, nostr.Tags{})
	case errors.As(err, &r):
		publishResponse(&models.Response{ResultType: method,
			Error: &models.Error{Code: r.Code, Message: r.Message, Detail: string(nipcash.ParseDetail(string(r.Detail)))}}, nostr.Tags{})
	default:
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Str("method", method).Msg("Withdrawal failed; nothing was committed")
		respondError(publishResponse, method, constants.ERROR_INTERNAL, fallback)
	}
}

// HandleCashWithdrawQuoteEvent serves cash_withdraw_quote (NIP-CASH §Quoting a
// Withdrawal). It writes nothing.
func (controller *nip47Controller) HandleCashWithdrawQuoteEvent(ctx context.Context, nip47Request *models.Request, requestEventID uint, app *db.App,
	publishResponse publishFunc, caller cashwithdraw.Caller) {
	method := nip47Request.Method
	if app.Kind != db.AppKindCashWallet {
		respondError(publishResponse, method, constants.ERROR_RESTRICTED, "cash_withdraw_quote requires a cash_wallet app")
		return
	}
	if controller.withdraw == nil {
		respondError(publishResponse, method, constants.ERROR_NOT_IMPLEMENTED, "this hub does not offer on-chain withdrawals")
		return
	}
	res, err := controller.withdraw.Quote(ctx, app, caller, nip47Request.Params)
	if err != nil {
		respondWithdrawError(publishResponse, method, app, err, "quote unavailable")
		return
	}
	publishResponse(&models.Response{ResultType: method, Result: res}, nostr.Tags{})
}

// HandleCashWithdrawEvent serves cash_withdraw (NIP-CASH §Processing
// Algorithm): it commits the payout and answers it queued, with its receipt.
func (controller *nip47Controller) HandleCashWithdrawEvent(ctx context.Context, nip47Request *models.Request, requestEventID uint, app *db.App,
	publishResponse publishFunc, call *PrivateCall, caller cashwithdraw.Caller, meta cashwithdraw.CallMeta) {
	method := nip47Request.Method
	if app.Kind != db.AppKindCashWallet {
		respondError(publishResponse, method, constants.ERROR_RESTRICTED, "cash_withdraw requires a cash_wallet app")
		return
	}
	if controller.withdraw == nil {
		respondError(publishResponse, method, constants.ERROR_NOT_IMPLEMENTED, "this hub does not offer on-chain withdrawals")
		return
	}
	if call != nil {
		caller.ItemSigner = call.ItemSigner
	}
	payout, err := controller.withdraw.Withdraw(ctx, app, caller, meta, nip47Request.Params, nil)
	if err != nil {
		respondWithdrawError(publishResponse, method, app, err, "the withdrawal could not be committed")
		return
	}
	publishResponse(&models.Response{ResultType: method, Result: payout}, nostr.Tags{})
}

// addWithdrawStatus adds cash_status' withdrawal fields: the hub's withdraw
// object while it offers withdrawals, and on each row a cash_withdraw ended the
// call's request_id while its record is kept, for its authorizer only (a
// cash-mode caller carries no proof and never gets it).
func (controller *nip47Controller) addWithdrawStatus(app *db.App, claims []db.CashWalletClaim, result *nipcash.CashStatusResult, caller *CashStatusCaller) {
	if controller.withdraw == nil {
		return
	}
	if app.ParentAppID != nil {
		result.Withdraw = controller.withdraw.StatusTerms(*app.ParentAppID)
	}
	if caller == nil || caller.IsCash || caller.IdentityValue == "" || app.WalletPubkey == nil {
		return
	}
	for i, c := range claims {
		if c.ClaimedAt == nil || c.EndedAs != db.CashSliceStatusRedeemed && c.EndedAs != db.CashSliceStatusTransferred {
			continue
		}
		result.Recipients[i].WithdrawRequestID = cashwithdraw.RequestIDForAuthorizer(controller.db, *app.WalletPubkey, caller.IdentityValue, c.ID)
	}
}
