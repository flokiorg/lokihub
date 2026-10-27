package nip47

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"

	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/cashwithdraw"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/controllers"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/nip47/permissions"
)

// withdrawMethods are the on-chain withdrawal methods (NIP-CASH §Redeeming
// On-chain), served only while the bill's Cash Hub offers them.
var withdrawMethods = map[string]bool{
	constants.NIP47MethodCashWithdrawQuote: true,
	constants.NIP47MethodCashWithdraw:      true,
}

// SetWithdraw sets the on-chain withdrawal service. Call it before any event is
// handled; nil offers no withdrawals (committed payouts are still answered).
func (svc *nip47Service) SetWithdraw(w *cashwithdraw.Service) {
	svc.withdraw = w
	permissions.SetWithdrawOffered(func(app *db.App) bool { return svc.withdrawOffered(app) })
}

// withdrawSvc is the withdrawal service, or one that offers nothing but still
// reads the payouts this node keeps.
func (svc *nip47Service) withdrawSvc() *cashwithdraw.Service {
	if svc.withdraw != nil {
		return svc.withdraw
	}
	return &cashwithdraw.Service{DB: svc.db, Cfg: svc.cfg}
}

// withdrawOffered reports whether bill's Cash Hub offers on-chain withdrawals
// now (cashwithdraw.Service.Offered).
func (svc *nip47Service) withdrawOffered(bill *db.App) bool {
	if svc.withdraw == nil || bill.ParentAppID == nil {
		return false
	}
	return svc.withdraw.Offered(*bill.ParentAppID)
}

// serveWithdrawItem runs a cash_withdraw_quote or cash_withdraw item.
func (svc *nip47Service) serveWithdrawItem(ctx context.Context, controller controllers.WithdrawController, request *models.Request,
	requestEventID uint, app *db.App, publish func(*models.Response, nostr.Tags), call *controllers.PrivateCall,
	caller cashwithdraw.Caller, binding PrivateItemBinding) {
	switch request.Method {
	case constants.NIP47MethodCashWithdrawQuote:
		controller.HandleCashWithdrawQuoteEvent(ctx, request, requestEventID, app, publish, caller)
	case constants.NIP47MethodCashWithdraw:
		controller.HandleCashWithdrawEvent(ctx, request, requestEventID, app, publish, call, caller,
			cashwithdraw.CallMeta{Deadline: binding.NotAfter, ArrivalRelay: binding.ArrivalRelay})
	}
}

// maybeServeWithdrawCancel serves a cash_withdraw cancel request (NIP-CASH
// §Cancelling a Payout) as the cash_lookup of that call: its target is any bill
// a kept record names, destroyed or not; it marks nothing and writes nothing
// but the invalidation request. isCancel is false for anything else.
func (svc *nip47Service) maybeServeWithdrawCancel(item transport.Item, binding PrivateItemBinding, billSigner string, billSigned bool) (transport.Result, bool, bool) {
	if item.Method != constants.NIP47MethodCashWithdraw {
		return transport.Result{}, false, false
	}
	serving, smErr := nipcash.ServingMethod(item.Method, item.Params)
	if smErr == nil && serving != constants.NIP47MethodCashLookup {
		return transport.Result{}, false, false
	}
	refuse := func(code, message string) (transport.Result, bool, bool) {
		return transport.Result{ID: item.ID, ResultType: item.Method,
			Error: &transport.ResultError{Code: code, Message: message}}, true, true
	}

	// I2 and I3, as a lookup's.
	keys, err := svc.lookupTargetKeys(item.Target)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to resolve a cancel request's target")
		omitted(item, "could not resolve the cancel target")
		return transport.Result{}, false, true
	}
	if len(keys) == 0 || !billSigned || !billProofLive(billSigner, keys) {
		omitted(item, "cancel request bill proof missing or not signed by a live connection of the target")
		return transport.Result{}, false, true
	}
	if smErr != nil {
		return refuse(constants.ERROR_BAD_REQUEST, "malformed cancel request")
	}
	// I8 as a lookup's.
	if !transport.BearerShapeOK(constants.NIP47MethodCashLookup, item.HasProof(), item.IsBearer()) {
		return refuse(constants.ERROR_BAD_REQUEST, "an item must carry either a slice proof or a cash secret, never both")
	}
	req, _, err := nipcash.ParseWithdrawCancel(item.Params)
	if err != nil || req == nil {
		return refuse(constants.ERROR_BAD_REQUEST, "malformed cancel request")
	}
	signer := ""
	if item.HasProof() {
		paramsHash, err := transport.CanonicalParamsHash(item.Params)
		if err == nil {
			signer, err = transport.VerifyItemProof(item.Proof, transport.ProofBinding{
				Target: item.Target, HubXOnly: binding.HubXOnly, Method: item.Method,
				ParamsHash: paramsHash, Nonce: binding.Nonce, NotAfter: binding.NotAfter,
			}, time.Now())
		}
		if err != nil {
			return refuse(constants.ERROR_UNAUTHORIZED, "this item's proof does not verify")
		}
	}
	limitKey := "lookup:" + signer
	if signer == "" {
		limitKey = "lookup-envelope:" + binding.Nonce
	}
	if svc.lookupLimiter != nil && !svc.lookupLimiter.Allow(limitKey, lookupsPerHour) {
		return refuse(constants.ERROR_RATE_LIMITED, "too many lookups; retry later")
	}

	rec, err := cashrequest.New(svc.db).Get(req.RequestID)
	if err != nil {
		return refuse(constants.ERROR_INTERNAL, "the hub could not read its records, please retry")
	}
	if rec == nil || rec.Method != constants.NIP47MethodCashWithdraw {
		return refuse(constants.ERROR_NOT_FOUND, "no withdrawal is recorded under this request_id")
	}
	if !recordAuthorizes(rec, item, signer, req.CashSecret) {
		return refuse(constants.ERROR_UNAUTHORIZED, "this cancel request does not match the call's record")
	}
	w, err := svc.withdrawSvc().Withdrawal(svc.db, rec)
	if err != nil {
		return refuse(constants.ERROR_INTERNAL, "the hub could not read its records, please retry")
	}
	if w == nil {
		return refuse(constants.ERROR_NOT_FOUND, "no withdrawal is recorded under this request_id")
	}
	w, err = svc.withdrawSvc().CancelRequest(w)
	if err != nil {
		var we *nipcash.WithdrawError
		if errors.As(err, &we) {
			return refuse(we.Code, we.Reason)
		}
		logger.Logger.Error().Err(err).Msg("Failed to apply a cancel request")
		return refuse(constants.ERROR_INTERNAL, "the hub could not apply this cancel request, please retry")
	}
	body, err := svc.payoutResult(w, rec, time.Now())
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to build a payout view")
		return refuse(constants.ERROR_INTERNAL, "the hub could not read this payout")
	}
	return transport.Result{ID: item.ID, ResultType: item.Method, Result: body}, true, true
}

// recordAuthorizes is a lookup's authorizer check against a record: its target
// is the item's, and the item proof's signer or the cash secret is its
// authorizer.
func recordAuthorizes(rec *db.CashRequestRecord, item transport.Item, signer, cashSecret string) bool {
	if rec.TargetWalletPubkey != item.Target {
		return false
	}
	if rec.AuthorizerKind == "commitment" {
		commitment, err := nipcash.CashCommitment(cashSecret)
		return !item.HasProof() && cashSecret != "" && err == nil &&
			subtle.ConstantTimeCompare([]byte(commitment), []byte(rec.Authorizer)) == 1
	}
	return signer != "" && signer == rec.Authorizer
}

// payoutResult is a withdrawal's payout, as the reply, a lookup's done and a
// cancel answer carry it: under cancelled with the re-minted bill's token,
// rebuilt and delivered as a lookup's (nested-encrypted to the claim signer, in
// the clear for cash; a destroyed bill's tombstone).
func (svc *nip47Service) payoutResult(w *db.CashWithdrawal, rec *db.CashRequestRecord, now time.Time) (json.RawMessage, error) {
	view, remint, err := svc.withdrawSvc().PayoutView(svc.db, w)
	if err != nil {
		return nil, err
	}
	if remint != nil {
		tok, err := svc.rebuildToken(remint, rec.DeliveryPubkey, now)
		if err != nil {
			return nil, err
		}
		view.NewWalletToken = tok
	}
	return json.Marshal(view)
}

// withdrawLookupResult is a cash_lookup done of a withdrawal: the payout's
// current state.
func (svc *nip47Service) withdrawLookupResult(rec *db.CashRequestRecord, now time.Time) (json.RawMessage, error) {
	w, err := svc.withdrawSvc().Withdrawal(svc.db, rec)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, errors.New("a withdraw record without its payout")
	}
	return svc.payoutResult(w, rec, now)
}

// tombstoneWithdrawRequestID is withdraw_request_id on a destroyed bill's
// tombstone: the cash_withdraw that ended a slice of it, while its record is
// kept, only for an item whose verified item proof is that call's authorizer.
func (svc *nip47Service) tombstoneWithdrawRequestID(item transport.Item, binding PrivateItemBinding) string {
	if !item.HasProof() {
		return ""
	}
	paramsHash, err := transport.CanonicalParamsHash(item.Params)
	if err != nil {
		return ""
	}
	signer, err := transport.VerifyItemProof(item.Proof, transport.ProofBinding{
		Target: item.Target, HubXOnly: binding.HubXOnly, Method: item.Method,
		ParamsHash: paramsHash, Nonce: binding.Nonce, NotAfter: binding.NotAfter,
	}, time.Now())
	if err != nil {
		return ""
	}
	return cashwithdraw.RequestIDForAuthorizer(svc.db, item.Target, signer, 0)
}
