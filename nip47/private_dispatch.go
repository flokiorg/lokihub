package nip47

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/controllers"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// PrivateItemBinding is what an item's proof must commit to, supplied by the caller that
// unwrapped the envelope.
//
// HubXOnly is the hub's IDENTITY key, not its inbox: the proof binds to the identity so an
// item cannot be replayed at another hub, while the envelope is encrypted to the inbox.
type PrivateItemBinding struct {
	HubXOnly string
	Nonce    string
	NotAfter int64
}

// ServePrivateItem runs one item from a private-transport envelope.
//
// served=false means OMIT the item — do not answer it at all. That is the protocol's rule
// rather than a convenience: an unknown target, a proof that did not verify, and a method
// the hub does not serve must be indistinguishable from outside, because distinguishing
// them would turn batching into an oracle for which bills a hub holds
// (NIP-CASH §Responses).
//
// served=true with an Error is the opposite case and is equally deliberate: the hub holds
// this bill, ran the item, and is declining. A caller needs that distinction to know
// whether retrying is meaningful — and for a circle join refused on membership or rate
// limit, NIP-CW §Private Join requires an error rather than silence.
//
// It lives here rather than in the service that unwraps envelopes because everything it
// needs — permissions, the rate limiters, the controllers themselves — already does. The
// alternative was reconstructing a thirteen-dependency controller elsewhere, which would
// have been a second place for that wiring to drift.
func (svc *nip47Service) ServePrivateItem(
	ctx context.Context,
	lnClient lnclient.LNClient,
	item transport.Item,
	binding PrivateItemBinding,
) (transport.Result, bool) {
	// 1. Servable at all? First because it is free, and because the allowlist must not
	// leak: an unserved method is omitted exactly like an unknown bill.
	if !IsPrivateServableMethod(item.Method) {
		return transport.Result{}, false
	}

	// 2. Resolve the bill by wallet_pubkey ALONE. There is no app_pubkey to use — the
	// envelope's outer key is ephemeral — which is why apps.wallet_pubkey carries its own
	// index; the composite one leads with app_pubkey and cannot serve this.
	var app db.App
	if err := svc.db.Where("wallet_pubkey = ?", item.Target).First(&app).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Logger.Error().Err(err).Msg("Failed to resolve a private item's target")
		}
		return transport.Result{}, false
	}

	// 3. Authorize the ITEM. The envelope proves nothing about who may act on a bill, since
	// its author is a throwaway key.
	if !svc.privateItemIsAuthorized(item, binding, &app) {
		return transport.Result{}, false
	}

	// 4. Scope, checked per item. Batching MUST NOT be a way around permissions.
	scope, err := permissions.RequestMethodToScope(item.Method)
	if err != nil {
		return transport.Result{}, false
	}
	if hasPermission, code, message := svc.permissionsService.HasPermission(&app, scope); !hasPermission {
		return transport.Result{
			ID:         item.ID,
			ResultType: item.Method,
			Error:      &transport.ResultError{Code: code, Message: message},
		}, true
	}

	// 5. One request-event row per item, keyed by envelope nonce plus item id.
	//
	// The controllers take a requestEventId and the standard path creates one row per Nostr
	// event — but a batch is one event carrying N operations, so sharing a row would leave
	// every audit record and failure log ambiguous about which of them it described. The
	// nonce is already unique per envelope and replay-checked, so nonce:id is unique
	// without inventing an identifier.
	requestEvent := db.RequestEvent{
		AppId:   &app.ID,
		NostrId: binding.Nonce + ":" + item.ID,
		Method:  item.Method,
		State:   db.REQUEST_EVENT_STATE_HANDLER_EXECUTING,
	}
	if err := svc.db.Create(&requestEvent).Error; err != nil {
		logger.Logger.Error().Err(err).Str("item", item.ID).Msg("Failed to record a private item")
		return transport.Result{}, false
	}

	// 6. The EXISTING controller. cash_redeem, cash_transfer and cash_consolidate carry the
	// money, and their atomicity and race guarantees hold precisely because there is one
	// implementation of each — a second for this transport would be a second thing to keep
	// correct.
	collector := &itemResponseCollector{}
	request := &models.Request{Method: item.Method, Params: item.Params}
	controller := controllers.NewNip47Controller(
		lnClient, svc.db, svc.eventPublisher, svc.permissionsService, svc.transactionsService,
		svc.appsService, svc.keys, svc.socialCache, svc.cashRateLimiter, svc.cashClaimLimiter,
		svc.circleRateLimiter, svc.cfg, svc.identityAuthorityMgr,
	)

	switch item.Method {
	case constants.NIP47MethodCashStatus, constants.NIP47MethodListRecipients:
		controller.HandleCashStatusEvent(ctx, request, requestEvent.ID, &app, collector.publish)
	case constants.NIP47MethodCashRedeem:
		controller.HandleCashRedeemEvent(ctx, request, requestEvent.ID, &app, collector.publish, nil)
	case constants.NIP47MethodCashTransfer:
		controller.HandleCashTransferEvent(ctx, request, requestEvent.ID, &app, collector.publish, nil)
	case constants.NIP47MethodCashConsolidate:
		controller.HandleCashConsolidateEvent(ctx, request, requestEvent.ID, &app, collector.publish, nil)
	case constants.NIP47MethodCreateCircleWallet:
		controller.HandleCreateCircleWalletEvent(ctx, request, requestEvent.ID, &app, collector.publish)
	default:
		// Unreachable: step 1 filtered to the allowlist. Omitted rather than panicking, so
		// a method added to the allowlist and forgotten here degrades to "not served"
		// instead of taking the subscription down.
		logger.Logger.Error().Str("method", item.Method).
			Msg("Private transport allowlist and dispatch disagree")
		return transport.Result{}, false
	}

	result, ok, err := collector.result(item.ID)
	if err != nil {
		logger.Logger.Error().Err(err).Str("item", item.ID).Msg("Failed to collect a private item's result")
		return transport.Result{}, false
	}
	return result, ok
}

// privateItemIsAuthorized checks one item's authorization against the bill it names.
//
// Two shapes, and the hub decides which applies from its OWN records rather than from what
// the item claims. That direction matters: an identity-bound bill could otherwise dodge its
// proof by omitting one and looking bearer (NIP-CASH §Bearer Items).
func (svc *nip47Service) privateItemIsAuthorized(
	item transport.Item,
	binding PrivateItemBinding,
	app *db.App,
) bool {
	// create_circle_wallet acts on a HUB rather than a bill, so there are no claims to
	// match a signer against — the kind-23199 identity proof inside its own params is what
	// authorizes it, and the controller checks that. Handled first because a hub
	// legitimately has no claims at all.
	isCircleJoin := item.Method == constants.NIP47MethodCreateCircleWallet

	claims, err := svc.appsService.ListClaimsForWallet(app.ID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to read a bill's claims")
		return false
	}

	// Cash-mode is a property of the BILL, read here, never asserted by the item.
	billIsCashMode := false
	for _, claim := range claims {
		if claim.IdentityType == db.CashIdentityCash {
			billIsCashMode = true
			break
		}
	}

	if len(item.Proof) == 0 {
		// Proofless is legitimate only for a genuinely cash-mode bill, whose secret in
		// params is the whole authorization. The controller still verifies that secret;
		// this only decides whether a missing proof is acceptable for this bill.
		return billIsCashMode
	}

	paramsHash, err := transport.CanonicalParamsHash(item.Params)
	if err != nil {
		return false
	}
	signer, err := transport.VerifyItemProof(item.Proof, transport.ProofBinding{
		Target:     item.Target,
		HubXOnly:   binding.HubXOnly,
		Method:     item.Method,
		ParamsHash: paramsHash,
		Nonce:      binding.Nonce,
		NotAfter:   binding.NotAfter,
	}, time.Now())
	if err != nil {
		return false
	}
	if isCircleJoin {
		return true
	}

	// The signer must actually hold a slice of this bill. A verified proof only shows
	// somebody signed for this binding; it does not show they are a recipient.
	for _, claim := range claims {
		if claim.IdentityType != db.CashIdentityCash && claim.IdentityValue == signer {
			return true
		}
	}
	return false
}
