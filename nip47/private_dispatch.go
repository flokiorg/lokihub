package nip47

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/flokiorg/lokihub/hubclock"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/cashwithdraw"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/controllers"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/ohstr/nmilat/nipcash"
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
	// ArrivalRelay is the relay the envelope arrived on: an order's notices
	// (NIP-CO §Order Notice) and a cash_withdraw's state notices go there.
	ArrivalRelay string
	// AuthFailures counts this envelope's items answered UNAUTHORIZED so far; once
	// over maxEnvelopeAuthFailures, its further value items are RATE_LIMITED.
	// Shared by every item of one envelope (items run one at a time); nil counts
	// nothing.
	AuthFailures *int
}

// maxEnvelopeAuthFailures is how many items of one envelope may fail
// authorization before its further value items are refused RATE_LIMITED.
const maxEnvelopeAuthFailures = 8

// valueMethods are the methods that move a slice's value.
var valueMethods = map[string]bool{
	constants.NIP47MethodCashRedeem:      true,
	constants.NIP47MethodCashTransfer:    true,
	constants.NIP47MethodCashConsolidate: true,
	constants.NIP47MethodCashWithdraw:    true,
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
	result, served := svc.servePrivateItem(ctx, lnClient, item, binding)
	if served && result.Error != nil && result.Error.Code == constants.ERROR_UNAUTHORIZED && binding.AuthFailures != nil {
		*binding.AuthFailures++
	}
	return result, served
}

// servePrivateItem is NIP-CASH §Serving an Envelope's I2 to I10 for one item (I1,
// the item's shape, was judged when the envelope was decoded).
func (svc *nip47Service) servePrivateItem(
	ctx context.Context,
	lnClient lnclient.LNClient,
	item transport.Item,
	binding PrivateItemBinding,
) (transport.Result, bool) {
	refuse := func(code, message string) (transport.Result, bool) {
		return transport.Result{ID: item.ID, ResultType: item.Method,
			Error: &transport.ResultError{Code: code, Message: message}}, true
	}

	// Verify the bill proof's SIGNATURE first, before the target is looked up.
	//
	// The verification is target-independent and is the expensive step, so doing it
	// here makes a missing bill and a foreign proof cost the same — see
	// billProofSigner for the timing oracle this closes. Who the signer must BE is
	// decided after the lookup (I3).
	signer, signed := svc.billProofSigner(item, binding)

	// cash_lookup resolves its own targets, which include bills the Hub has
	// destroyed and calls it has recorded (NIP-CASH §Looking Up a Call).
	if item.Method == constants.NIP47MethodCashLookup {
		return svc.servePrivateLookup(item, binding, signer, signed)
	}
	// A cash_withdraw cancel request is served as the cash_lookup of that call
	// (nipcash.ServingMethod), on the bill a kept record names.
	if result, served, isCancel := svc.maybeServeWithdrawCancel(item, binding, signer, signed); isCancel {
		return result, served
	}

	// The destroyed-bill record is read for every item, live bill or not, so a
	// target this hub never served costs no less than a live one.
	tombstone, tombstoned := db.SpentBillTombstoneFor(svc.db, item.Target)

	// I2: the target, by wallet_pubkey alone (the envelope's outer key is ephemeral).
	app, err := svc.resolveItemTarget(item.Target)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Logger.Error().Err(err).Msg("Failed to resolve a private item's target")
		}
		// No live bill: usually a target this hub never served, answered by silence;
		// a bill it DESTROYED whose retention window is open is held (I2), and,
		// once its bill proof verifies (I3), answered with its tombstone (I4).
		if !signed {
			omitted(item, "bill proof missing, malformed, or not verifiable")
			return transport.Result{}, false
		}
		if result, ok := svc.tryPrivateSpentBill(item, binding, signer, tombstone, tombstoned); ok {
			return result, true
		}
		omitted(item, "no bill with this target wallet_pubkey")
		return transport.Result{}, false
	}

	// I3: the bill proof must verify and be signed by the target's connection key.
	//
	// This is the gate the standard transport got for free: a request there was
	// encrypted to the bill's own wallet pubkey, so sending one at all demonstrated
	// possession. An envelope is encrypted to the hub's inbox and merely NAMES a
	// target, and a wallet pubkey is public. Failing it is an OMISSION, so a caller
	// who cannot prove possession learns nothing; once it passes, every answer below
	// is safe to give, since it tells the sender nothing they did not know.
	if !signed {
		omitted(item, "bill proof missing, malformed, or not verifiable")
		return transport.Result{}, false
	}
	if app.Kind == db.AppKindCircleHub {
		// A circle's bill proof verifies against any live connection of the
		// Hub: its own and its live listing connections (NIP-CW §The Circle
		// Wallet Hub Connection, H-CW-072; VerifyBillProofAny's rule).
		if !billProofLive(signer, svc.liveHubConnKeys(&app)) {
			omitted(item, "bill proof not signed by a live connection of this circle")
			return transport.Result{}, false
		}
	} else if !billProofHolder(signer, app.AppPubkey) {
		omitted(item, "bill proof not signed by this bill's connection key")
		return transport.Result{}, false
	}

	// I5: a method this hub does not serve on this transport is NOT_IMPLEMENTED.
	// An Order connection is the target of cash_order (and its lookups) only,
	// and cash_order targets nothing else (NIP-CO §What It Allows).
	if !IsPrivateServableMethod(item.Method) ||
		(app.Kind == db.AppKindCashOrder) != (item.Method == constants.NIP47MethodCashOrder) {
		return refuse(constants.ERROR_NOT_IMPLEMENTED, item.Method+" is not served on this transport")
	}
	// Both withdrawal methods are on the allowlist, but are NOT_IMPLEMENTED
	// while the bill's Cash Hub does not offer on-chain withdrawals.
	if withdrawMethods[item.Method] && !svc.withdrawOffered(&app) {
		return refuse(constants.ERROR_NOT_IMPLEMENTED, item.Method+" is not offered by this hub")
	}

	// I6 and I7: a call's request_id must be present and well formed (BAD_REQUEST),
	// then unused (REQUEST_REUSED), before its proofs are looked at. Only checked
	// here; the method marks it used once the call is authorized.
	if refusal, refused := svc.refuseRequestID(item); refused {
		return refusal, true
	}

	// I8 is nmilat's BearerShapeOK: never both a proof and a cash_secret; a value
	// method needs one; cash_status never carries a cash_secret.
	if !transport.BearerShapeOK(item.Method, item.HasProof(), item.IsBearer()) {
		message := "this item's proof and cash_secret do not fit " + item.Method
		if item.HasProof() && item.IsBearer() {
			message = "an item must carry either a slice proof or a cash secret, never both"
		}
		return refuse(constants.ERROR_BAD_REQUEST, message)
	}

	// An envelope whose items keep failing authorization runs no more value items:
	// a cash secret or proof being guessed is bounded per envelope, never per bill.
	if valueMethods[item.Method] && binding.AuthFailures != nil && *binding.AuthFailures > maxEnvelopeAuthFailures {
		return refuse(constants.ERROR_RATE_LIMITED, "too many items of this envelope failed authorization")
	}

	// I9: authorize the ITEM. The envelope proves nothing about who may act on a
	// bill, since its author is a throwaway key.
	//
	// The authorized identity comes back because cash_status needs it: "mine" MUST be
	// decided from the proof's own signer, never from anything the item asserts
	// (NIP-CASH §Scoping the Roster).
	// The connection the bill proof came through: the hub's own, or a listed
	// circle's listing connection, which the join record names (WP-14, WP-25).
	connUsed := signer
	signer, billIsCashMode, authorized, lookupErr := svc.privateItemIsAuthorized(item, binding, &app)
	if lookupErr != nil {
		// The hub could not read its own records, which says nothing about the
		// sender: answer with a code a client retries, not a refusal. Circle
		// joins use NIP-CW's NOT_READY.
		code := constants.ERROR_INTERNAL
		if item.Method == constants.NIP47MethodCreateCircleWallet {
			code = constants.ERROR_NOT_READY
		}
		return refuse(code, "the hub could not read this wallet's records, please retry")
	}
	if !authorized {
		switch {
		case item.HasProof() && signer == "":
			// The item proof did not verify.
			return refuse(constants.ERROR_UNAUTHORIZED, "this item's proof does not verify")
		case !item.HasProof() && !billIsCashMode:
			// A proofless item on a slice that is not cash-mode by the hub's records.
			return refuse(constants.ERROR_UNAUTHORIZED, "this bill is identity-bound and the item carries no slice proof")
		case !item.HasProof() && itemCashSecretMalformed(item):
			// A cash secret in any form but 64 lowercase hex is BAD_REQUEST
			// (NIP-CASH §Cash-Mode Slices).
			return refuse(constants.ERROR_BAD_REQUEST, "cash_secret must be 64 lowercase hex characters")
		case !item.HasProof():
			// A cash secret that does not verify is UNAUTHORIZED (NIP-CASH §Errors,
			// §Bearer Items), never NOT_FOUND.
			return refuse(constants.ERROR_UNAUTHORIZED, "this cash secret does not verify for this bill")
		case item.Method == constants.NIP47MethodCreateCircleWallet:
			return refuse(constants.ERROR_UNAUTHORIZED, "a join's item proof must be signed by the pubkey that is joining")
		case claimSignerDiffers(item, signer):
			// For a call carrying a claim proof, an item proof by any key but
			// the claim proof's signer is one that does not verify (NIP-CASH
			// §Item Proofs): UNAUTHORIZED, not "no slice for this signer".
			return refuse(constants.ERROR_UNAUTHORIZED, "the item proof was not signed by the claim proof's signer")
		default:
			// The proof verified, but its signer holds no slice of this bill.
			return refuse(constants.ERROR_NOT_FOUND, "no slice of this bill is registered for the identity that signed this item")
		}
	}

	// An order is placed on a connection that grants nothing else: the app kind
	// is its whole permission.
	if item.Method == constants.NIP47MethodCashOrder {
		return svc.servePrivateOrder(ctx, lnClient, item, binding, &app, signer)
	}

	// 4. Scope, checked per item. Batching MUST NOT be a way around permissions.
	scope, err := permissions.RequestMethodToScope(item.Method)
	if err != nil {
		omitted(item, "method maps to no permission scope")
		return transport.Result{}, false
	}
	if hasPermission, code, message := svc.permissionsService.HasPermission(&app, scope); !hasPermission &&
		!svc.statusOfExpiredBill(&app, item.Method, scope) {
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
	if beforeRecordPrivateItem != nil {
		beforeRecordPrivateItem(&app)
	}
	if err := svc.recordPrivateItem(&requestEvent); err != nil {
		// The target went between I2 and here: a drained bill is deleted after
		// its last payout settles, asynchronously (the redeem outcome consumer),
		// or by a sibling item's split in the same envelope, so an item for it
		// may arrive while that runs. Served again from the top, it is answered
		// as a destroyed bill (its tombstone, or omitted).
		if !reservedAfterTargetGone(ctx) && (errors.Is(err, errItemTargetGone) || !svc.appExists(app.ID)) {
			logger.Logger.Info().Str("item", item.ID).Msg("A private item's target was deleted while it was served; serving it again")
			return svc.servePrivateItem(context.WithValue(ctx, targetGoneKey{}, true), lnClient, item, binding)
		}
		logger.Logger.Error().Err(err).Str("item", item.ID).Msg("Failed to record a private item")
		omitted(item, "could not record the request event")
		return transport.Result{}, false
	}

	// The row records how the handler ended, as the standard path's publishResponse
	// does: executed once it answered, an error answer included, and error when it
	// produced nothing usable. The reply itself goes out later, once per envelope, so a
	// delivery failure is logged there rather than here.
	finalState := db.REQUEST_EVENT_STATE_HANDLER_ERROR
	defer func() {
		if err := svc.db.Model(&requestEvent).Update("state", finalState).Error; err != nil {
			logger.Logger.Error().Err(err).Str("item", item.ID).Msg("Failed to save a private item's request-event state")
		}
	}()

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
	).WithPrivateIdentity(svc.privateIdentity).WithLineageLimiter(svc.cashLineageLimiter)

	switch item.Method {
	case constants.NIP47MethodCashStatus:
		controller.WithWithdraw(svc.withdrawSvc()).HandleCashStatusEvent(ctx, request, requestEvent.ID, &app, collector.publish,
			&controllers.CashStatusCaller{IdentityValue: signer, IsCash: billIsCashMode})
	case constants.NIP47MethodCashRedeem:
		controller.HandleCashRedeemEvent(ctx, request, requestEvent.ID, &app, collector.publish, nil,
			&controllers.PrivateCall{Meta: privateCallMeta(binding), ItemSigner: signer})
	case constants.NIP47MethodCashTransfer:
		controller.HandleCashTransferEvent(ctx, request, requestEvent.ID, &app, collector.publish, nil,
			&controllers.PrivateCall{Meta: privateCallMeta(binding), ItemSigner: signer})
	case constants.NIP47MethodCashConsolidate:
		controller.HandleCashConsolidateEvent(ctx, request, requestEvent.ID, &app, collector.publish, nil,
			&controllers.PrivateCall{Meta: privateCallMeta(binding), ItemSigner: signer})
	case constants.NIP47MethodCreateCircleWallet:
		// The join record names the connection the bill proof came through:
		// the hub's own, or a listed circle's listing connection (WP-14, WP-25).
		joinHub := app
		joinHub.AppPubkey = connUsed
		controller.HandleCreateCircleWalletEvent(ctx, request, requestEvent.ID, &joinHub, collector.publish)
	case constants.NIP47MethodCashWithdrawQuote, constants.NIP47MethodCashWithdraw:
		call := &controllers.PrivateCall{Meta: privateCallMeta(binding), ItemSigner: signer}
		withdrawCaller := cashwithdraw.Caller{ItemSigner: signer, CashMode: billIsCashMode, LimitKey: signer}
		if signer == "" {
			withdrawCaller.LimitKey = "envelope:" + binding.Nonce
		}
		svc.serveWithdrawItem(ctx, controller.WithWithdraw(svc.withdrawSvc()), request, requestEvent.ID, &app,
			collector.publish, call, withdrawCaller, binding)
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
	if ok {
		finalState = db.REQUEST_EVENT_STATE_HANDLER_EXECUTED
	}
	return result, ok
}

// tryPrivateSpentBill answers a private-transport item naming a bill this hub
// destroyed, with the same "spent" tombstone the standard transport gives
// (NIP-CASH §Answering About a Destroyed Bill).
//
// Silence alone cannot be told apart from a hub that is slow or unreachable, which
// forces every client to choose a wrong default: treat the silence as retryable and
// a genuinely spent bill retries forever, or treat it as gone and an outage tells
// someone their funds are lost. So the hub answers definitively while it still can.
//
// Three gates, the same three tryReplySpentBill applies, differing only in how the
// third is proved:
//
//  1. the wallet pubkey names a bill THIS hub archived;
//  2. the hub's own retention window has not elapsed;
//  3. the sender holds the bill — here, a kind-23193 bill proof signed by the
//     pairing key re-derived from the archived bill's app id. On the standard
//     transport the same fact came from the request being encrypted to the bill.
//
// Gate 3 is load-bearing: without it this is the existence oracle that deleting the
// bill exists to remove. Note it is re-derived rather than stored — the archive
// keeps the app id, and the pairing key is deterministic from it, so a destroyed
// bill needs no secret retained to stay verifiable.
//
// cash_status and the value methods are answered (I4), each under its own
// result_type: the tombstone is a result, never an error.
func (svc *nip47Service) tryPrivateSpentBill(item transport.Item, binding PrivateItemBinding, signer string, tombstone db.SpentBillTombstone, ok bool) (transport.Result, bool) {
	// Gate 2, using db.RetentionWindowOpen rather than a local comparison so this
	// path and the relay gate agree exactly at the boundary — they did not when each
	// wrote the comparison itself.
	// The record was read by the caller, for every cash_status item (see ServePrivateItem).
	retainedUntil := tombstone.RetainedUntil
	if !ok || !db.RetentionWindowOpen(retainedUntil, hubclock.Now()) {
		return transport.Result{}, false
	}

	// Gate 3, against the pubkey stored at archive time: deriving it here cost
	// enough to tell a recently destroyed bill from one never served.
	if tombstone.ConnectionPubkey == "" || !billProofHolder(signer, tombstone.ConnectionPubkey) {
		return transport.Result{}, false
	}

	// I4: cash_status and the value methods, the withdrawal methods included,
	// are answered with the tombstone.
	// I5: a method this transport does not serve is NOT_IMPLEMENTED; any other
	// method has nothing to act on and is omitted.
	if !nipcash.AnswersWithTombstone(item.Method) {
		if !IsPrivateServableMethod(item.Method) {
			return transport.Result{ID: item.ID, ResultType: item.Method, Error: &transport.ResultError{
				Code: constants.ERROR_NOT_IMPLEMENTED, Message: item.Method + " is not served on this transport"}}, true
		}
		return transport.Result{}, false
	}

	deadline := retainedUntil.Unix()
	body, err := json.Marshal(nipcash.Tombstone{
		Status:            nipcash.StatusSpent,
		Reason:            tombstone.Reason,
		RetainedUntil:     &deadline,
		WithdrawRequestID: svc.tombstoneWithdrawRequestID(item, binding),
	})
	if err != nil {
		logger.Logger.Error().Err(err).Str("walletPubkey", item.Target).
			Msg("Failed to build a private-transport spent-bill reply")
		return transport.Result{}, false
	}
	result := transport.Result{ID: item.ID, ResultType: item.Method, Result: body}

	logger.Logger.Debug().
		Str("walletPubkey", item.Target).
		Time("retainedUntil", retainedUntil).
		Msg("Answered a private-transport request for a spent cash bill")
	return result, true
}

// billProofValid checks an item's kind-23193 bill proof against the bill's own
// connection pubkey.
//
// wantPubkey is db.App.AppPubkey for a live bill, which is the deterministic
// pairing pubkey derived from the bill's app id (cashwallet.Create) — i.e. exactly
// the pubkey of the connection secret inside the bill's token. For a destroyed bill
// the same pubkey is read from the archive; see tryPrivateSpentBill.
//
// The KIND matters as much as the signature. A slice proof binds to the same six
// fields, so accepting either kind here would let anyone who can sign anything
// claim possession — the kind is the only thing separating "I control a key" from
// "I hold this bill". transport.VerifyBillProof enforces it.
// billProofSigner verifies the kind-23193 bill proof and returns who signed it.
//
// Split out from the comparison deliberately, and called BEFORE the bill is looked
// up, because the signature verification is the expensive part (~373us of secp256k1)
// and it does not depend on the bill at all. Doing it first makes every omission cost
// the same.
//
// It used to run after the lookup, which made the two information-free omissions
// distinguishable by TIMING: a target naming no bill missed the lookup and returned
// immediately having verified nothing, while a target naming a real bill with a
// foreign proof paid the full verification before failing. Both say nothing, but they
// did not say it at the same speed — so an attacker who supplies a structurally
// perfect proof signed with their own key could measure the difference and enumerate
// which wallet pubkeys this hub actually serves. That is precisely the existence
// oracle the omission exists to prevent.
//
// Returns false for a missing or malformed proof too, so an item that cannot possibly
// authorize never reaches the database.
func (svc *nip47Service) billProofSigner(item transport.Item, binding PrivateItemBinding) (string, bool) {
	if !item.HasBillProof() {
		return "", false
	}
	paramsHash, err := transport.CanonicalParamsHash(item.Params)
	if err != nil {
		return "", false
	}
	signer, err := transport.VerifyBillProof(item.BillProof, transport.ProofBinding{
		Target:     item.Target,
		HubXOnly:   binding.HubXOnly,
		Method:     item.Method,
		ParamsHash: paramsHash,
		Nonce:      binding.Nonce,
		NotAfter:   binding.NotAfter,
	}, time.Now())
	if err != nil {
		return "", false
	}
	return signer, true
}

// beforeRecordPrivateItem is a test seam run just before an item's request
// event is written; nil in production.
var beforeRecordPrivateItem func(app *db.App)

// targetGoneKey marks a context whose item is being served again because its
// target was deleted while it was first served (it is served again once only).
type targetGoneKey struct{}

func reservedAfterTargetGone(ctx context.Context) bool {
	v, _ := ctx.Value(targetGoneKey{}).(bool)
	return v
}

// statusOfExpiredBill reports a cash_status on a cash wallet that holds the
// scope and is refused only because its deadline passed: NIP-CASH answers
// cash_status with the roster and its expires_at whatever the expiry (only
// the spending methods refuse a slice past its expiry, RESTRICTED), so a
// client can read the deadline that passed.
func (svc *nip47Service) statusOfExpiredBill(app *db.App, method, scope string) bool {
	if method != constants.NIP47MethodCashStatus || app.Kind != db.AppKindCashWallet {
		return false
	}
	var n int64
	if err := svc.db.Model(&db.AppPermission{}).Where("app_id = ? AND scope = ?", app.ID, scope).Count(&n).Error; err != nil {
		return false
	}
	return n > 0
}

// appExists reports whether the app row id is still there; a read error counts
// as there (the caller then keeps its original error).
// errItemTargetGone: the item's target app was deleted before its request
// event could be recorded.
var errItemTargetGone = errors.New("the item's target was deleted")

// recordPrivateItem inserts ev once its app is read under a share lock (on
// sqlite the IMMEDIATE transaction holds the write lock), so a concurrent
// delete of the target either commits first, giving errItemTargetGone, or
// waits; the insert never fails on the app's foreign key.
func (svc *nip47Service) recordPrivateItem(ev *db.RequestEvent) error {
	return svc.db.Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&db.App{}).Select("id").Where("id = ?", *ev.AppId)
		if tx.Name() == "postgres" {
			q = q.Clauses(clause.Locking{Strength: "SHARE"})
		}
		var ids []uint
		if err := q.Find(&ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return errItemTargetGone
		}
		return tx.Create(ev).Error
	})
}

func (svc *nip47Service) appExists(id uint) bool {
	var n int64
	if err := svc.db.Model(&db.App{}).Where("id = ?", id).Count(&n).Error; err != nil {
		return true
	}
	return n > 0
}

// resolveItemTarget is I2: the app whose wallet_pubkey is target. Several
// connections of one circle share the Hub's wallet pubkey, and the circle_hub
// row is the one an item targets (H-CW-072).
func (svc *nip47Service) resolveItemTarget(target string) (db.App, error) {
	var apps []db.App
	// A listed circle's own connection shares its hub's wallet pubkey and is
	// served by the hub (WP-25 item 6), so it never resolves.
	if err := svc.db.Where("wallet_pubkey = ? AND kind <> ?", target, db.AppKindCircleHubConn).Order("id").Limit(8).Find(&apps).Error; err != nil {
		return db.App{}, err
	}
	if len(apps) == 0 {
		return db.App{}, gorm.ErrRecordNotFound
	}
	for _, app := range apps {
		if app.Kind == db.AppKindCircleHub {
			return app, nil
		}
	}
	return apps[0], nil
}

// liveHubConnKeys are the client keys of a circle Hub's live connections: its own
// connection and its listing connections (WP-25). A revoked listing connection's
// row is deleted, so its key is never passed.
func (svc *nip47Service) liveHubConnKeys(hub *db.App) []string {
	keys := []string{hub.AppPubkey}
	if hub.WalletPubkey == nil {
		return keys
	}
	var listing []string
	if err := svc.db.Model(&db.App{}).Where("wallet_pubkey = ? AND kind = ?", *hub.WalletPubkey, db.AppKindCircleHubConn).
		Order("id").Pluck("app_pubkey", &listing).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to read a circle's listing connections")
		return keys
	}
	return append(keys, listing...)
}

// billProofLive is transport.VerifyBillProofAny's rule for a signer
// billProofSigner already verified: the bill proof must be signed by one of
// live, the client keys of the target's live connections. VerifyBillProofAny is
// VerifyBillProof followed by exactly this membership check; the signature half
// runs before the target is looked up (see billProofSigner), so a found target
// and a missing one cost the same, and only this half runs here.
func billProofLive(signer string, live []string) bool {
	return signer != "" && slices.Contains(live, signer)
}

// billProofHolder reports whether an already-verified signer is the key this bill's
// proof must carry. Free: the expensive work happened in billProofSigner.
func billProofHolder(signer, wantPubkey string) bool {
	return wantPubkey != "" && signer == wantPubkey
}

// cashSecretMatchesBill reports whether a proofless item's cash_secret is the secret
// behind one of claims' cash-mode slices, claimed or not: only its SHA-256 is stored.
// itemCashSecretMalformed reports whether item's params carry a cash_secret that
// is not 64 lowercase hex characters.
// claimSignerDiffers reports an item of a method whose item proof must be
// signed by its claim proof's signer (cash_redeem, cash_withdraw, cash_transfer,
// and cash_consolidate's first source) carrying a claim proof by a key other
// than signer. An item with no claim proof (a cash_withdraw cancel request, a
// cash-mode call) or one that does not parse reports false.
func claimSignerDiffers(item transport.Item, signer string) bool {
	if !valueMethods[item.Method] {
		return false
	}
	var params struct {
		IdentityEvent string `json:"identity_event"`
		Sources       []struct {
			IdentityEvent string `json:"identity_event"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(item.Params, &params); err != nil {
		return false
	}
	raw := params.IdentityEvent
	if item.Method == constants.NIP47MethodCashConsolidate {
		raw = ""
		if len(params.Sources) > 0 {
			raw = params.Sources[0].IdentityEvent
		}
	}
	if raw == "" {
		return false
	}
	var ev struct {
		PubKey string `json:"pubkey"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil || ev.PubKey == "" {
		return false
	}
	return !strings.EqualFold(ev.PubKey, signer)
}

func itemCashSecretMalformed(item transport.Item) bool {
	var params struct {
		CashSecret *string `json:"cash_secret"`
	}
	if err := json.Unmarshal(item.Params, &params); err != nil || params.CashSecret == nil {
		return false
	}
	return !nipcash.ValidCashSecret(*params.CashSecret)
}

func cashSecretMatchesBill(item transport.Item, claims []db.CashWalletClaim) bool {
	var params struct {
		CashSecret string `json:"cash_secret"`
	}
	if err := json.Unmarshal(item.Params, &params); err != nil || !nipcash.ValidCashSecret(params.CashSecret) {
		return false
	}
	secret, err := hex.DecodeString(params.CashSecret)
	if err != nil || len(secret) == 0 {
		return false
	}
	sum := sha256.Sum256(secret)
	commitment := []byte(hex.EncodeToString(sum[:]))
	for _, claim := range claims {
		if claim.IdentityType == db.CashIdentityCash &&
			subtle.ConstantTimeCompare(commitment, []byte(strings.ToLower(claim.IdentityValue))) == 1 {
			return true
		}
	}
	return false
}

// privateItemIsAuthorized checks one item's authorization against the bill it names, and
// reports WHO it authorized.
//
// Two shapes, and the hub decides which applies from its OWN records rather than from what
// the item claims. That direction matters: an identity-bound bill could otherwise dodge its
// proof by omitting one and looking bearer (NIP-CASH §Bearer Items).
//
// signer is the verified proof's own signer, empty for a proofless cash-mode item — there is
// no key to recover there, since the secret in params is the whole authorization.
// billIsCashMode is read from the hub's own claim rows, never from the item.
// lookupErr reports that those rows could not be read, which decides nothing.
func (svc *nip47Service) privateItemIsAuthorized(
	item transport.Item,
	binding PrivateItemBinding,
	app *db.App,
) (signer string, billIsCashMode bool, authorized bool, lookupErr error) {
	// create_circle_wallet acts on a HUB rather than a bill, so there are no claims to
	// match a signer against — the kind-23199 identity proof inside its own params is what
	// authorizes it, and the controller checks that. Handled first because a hub
	// legitimately has no claims at all.
	isCircleJoin := item.Method == constants.NIP47MethodCreateCircleWallet
	if item.Method == constants.NIP47MethodCashOrder {
		// An order acts on an Order connection, which has no slices: its item
		// proof's signer is the buyer key, the order's authorizer (NIP-CO §The
		// Item). I8 already required the proof.
		paramsHash, err := transport.CanonicalParamsHash(item.Params)
		if err != nil {
			return "", false, false, nil
		}
		signer, err := transport.VerifyItemProof(item.Proof, transport.ProofBinding{
			Target: item.Target, HubXOnly: binding.HubXOnly, Method: item.Method,
			ParamsHash: paramsHash, Nonce: binding.Nonce, NotAfter: binding.NotAfter,
		}, time.Now())
		if err != nil {
			return "", false, false, nil
		}
		return signer, false, true, nil
	}

	claims, err := svc.appsService.ListClaimsForWallet(app.ID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to read a bill's claims")
		return "", false, false, err
	}

	// Cash-mode is a property of the BILL, read here, never asserted by the item.
	for _, claim := range claims {
		if claim.IdentityType == db.CashIdentityCash {
			billIsCashMode = true
			break
		}
	}

	if !item.HasProof() {
		// Proofless is legitimate only for a genuinely cash-mode bill, whose secret in
		// params is the whole authorization. The controller still verifies that secret;
		// this only decides whether a missing proof is acceptable for this bill.
		//
		// HasProof, not a length check: JSON has three spellings of "no proof" —
		// absent, empty, and the literal `null` — and the last decodes to four bytes.
		// A length check read that as a proof present-but-unverifiable, so every
		// proofless bearer item was refused and cash-mode bills could not be served
		// over this transport at all.
		//
		// A cash_status on a cash-mode bill carries neither proof nor secret and is
		// answered from the bill proof alone (NIP-CASH §Bearer Items, §Scoping the
		// Roster); I8 already refused one carrying a secret.
		if (item.Method == constants.NIP47MethodCashStatus || item.Method == constants.NIP47MethodCashWithdrawQuote) && !item.IsBearer() {
			return "", billIsCashMode, billIsCashMode, nil
		}
		// Otherwise the secret must match one of this bill's cash-mode slices
		// (NIP-CASH §Bearer Items), checked here once for every method.
		return "", billIsCashMode, billIsCashMode && cashSecretMatchesBill(item, claims), nil
	}

	paramsHash, err := transport.CanonicalParamsHash(item.Params)
	if err != nil {
		return "", billIsCashMode, false, nil
	}
	signer, err = transport.VerifyItemProof(item.Proof, transport.ProofBinding{
		Target:     item.Target,
		HubXOnly:   binding.HubXOnly,
		Method:     item.Method,
		ParamsHash: paramsHash,
		Nonce:      binding.Nonce,
		NotAfter:   binding.NotAfter,
	}, time.Now())
	if err != nil {
		return "", billIsCashMode, false, nil
	}
	if isCircleJoin {
		// NIP-CW §Private Join: the item proof's signer MUST be the joining pubkey.
		// Otherwise whoever assembled the envelope could keep a member's 23199 proof
		// and re-point the join at params of its own choosing.
		var join struct {
			Pubkey string `json:"pubkey"`
		}
		if err := json.Unmarshal(item.Params, &join); err != nil || join.Pubkey != signer {
			return signer, billIsCashMode, false, nil
		}
		return signer, billIsCashMode, true, nil
	}

	// The signer should hold a slice of this bill: a verified proof only shows somebody
	// signed for this binding, not that they are a recipient.
	//
	// This check is an EARLY-OUT, not the authorization boundary — the controller
	// authorizes, and does so for every identity mode. It exists so a possessor who holds
	// no slice gets a definite NOT_FOUND rather than travelling further.
	//
	// It can only be decided here for a PUBKEY claim, whose identity_value IS the signer's
	// pubkey. A connection_key claim's identity_value is
	// hex(sha256(platform + ":" + externalID)) (§Terminology) — never a pubkey — so the
	// signer can never equal it, and the binding from signer to connection key lives in the
	// IA attestation inside params, which only the controller parses.
	//
	// Matching on identity_value alone therefore refused EVERY connection_key slice. With
	// the private transport now the only one for bill methods, that made the whole identity
	// mode unredeemable and its funds unreachable — caught by
	// TestClaimFunds/ConnectionKeyMode_ClaimHappyPath once that test was moved onto this
	// transport. So when the bill has any non-pubkey claim, this layer must not pretend to
	// know: defer to the controller rather than guess.
	// cash_consolidate is exempt for a different reason than the identity modes below: its
	// authorization is explicitly **per source**, not per connection. NIP-CASH
	// §Consolidating Tokens: "the calling connection's own identity need not match, or even
	// be among, the sources being consolidated — the calling connection is only an entry
	// point, and each source's own proof is what actually authorizes moving it."
	//
	// So requiring the signer to hold a slice of the ENTRY-POINT bill refused every
	// consolidation by a caller who is not also one of its recipients — a shape the spec
	// deliberately permits, and which the connection-key source tests exercise.
	if item.Method == constants.NIP47MethodCashConsolidate {
		return signer, billIsCashMode, true, nil
	}

	decidableHere := true
	for _, claim := range claims {
		if claim.IdentityType != db.CashIdentityCash && claim.IdentityType != db.CashIdentityPubkey {
			decidableHere = false
			break
		}
	}
	// On a bill with a connection_key slice the signer cannot be matched here, so a
	// value method goes to its controller, which authorizes it itself. cash_status
	// does not: its controller performs no identity check. NIP-CASH §Scoping the
	// Roster decides "mine" from the item proof's own signer, never from anything the
	// item asserts (so no attestation_event), and a caller whose proof verifies but
	// who holds no slice MUST get NOT_FOUND, never a wider roster, whatever the scope.
	// So cash_status falls through to the signer match below.
	if !decidableHere && item.Method != constants.NIP47MethodCashStatus {
		return signer, billIsCashMode, true, nil
	}
	for _, claim := range claims {
		if claim.IdentityType != db.CashIdentityCash && claim.IdentityValue == signer {
			return signer, billIsCashMode, true, nil
		}
	}
	// Verified, but the signer holds no slice: the signer comes back so the caller
	// can tell this NOT_FOUND from a proof that did not verify.
	return signer, billIsCashMode, false, nil
}

// requestIDMethods are the private methods that are calls carrying a request_id
// checked at I6 and I7.
var requestIDMethods = map[string]bool{
	constants.NIP47MethodCashOrder:       true,
	constants.NIP47MethodCashTransfer:    true,
	constants.NIP47MethodCashConsolidate: true,
	constants.NIP47MethodCashWithdraw:    true,
}

// refuseRequestID is I6 and I7 for a call: a missing or malformed request_id is
// BAD_REQUEST, a used one REQUEST_REUSED.
func (svc *nip47Service) refuseRequestID(item transport.Item) (transport.Result, bool) {
	if !requestIDMethods[item.Method] {
		return transport.Result{}, false
	}
	refuse := func(code, message string) (transport.Result, bool) {
		return transport.Result{ID: item.ID, ResultType: item.Method,
			Error: &transport.ResultError{Code: code, Message: message}}, true
	}
	var params struct {
		RequestID *string `json:"request_id"`
	}
	if json.Unmarshal(item.Params, &params) != nil || params.RequestID == nil || !nipcash.ValidRequestID(*params.RequestID) {
		return refuse(constants.ERROR_BAD_REQUEST, "request_id is required: 32 to 64 lowercase hex characters")
	}
	used, err := cashrequest.New(svc.db).IsUsed(*params.RequestID)
	if err != nil {
		logger.Logger.Error().Err(err).Str("item", item.ID).Msg("Failed to read a request_id mark")
		return refuse(constants.ERROR_INTERNAL, "the hub could not read its records, please retry")
	}
	if used {
		return refuse(constants.ERROR_REQUEST_REUSED, "this request_id was already used on this node")
	}
	return transport.Result{}, false
}

// privateCallMeta is an item's call metadata: its deadline is the envelope's
// not_after, which E4 already bounded by H.
func privateCallMeta(binding PrivateItemBinding) models.CallMeta {
	return models.CallMeta{Transport: models.TransportPrivate, ArrivedAt: time.Now().Unix(),
		Deadline: binding.NotAfter, DeadlinePresent: true}
}

// omitted records WHY an item was dropped, at Debug only.
//
// The caller is told nothing — an omission must stay information-free, or batching
// becomes an oracle for which bills a hub holds — so this changes nothing on the
// wire. It exists because the hub is the ONLY side that can know: a client sees
// four different faults as one silence by design, so without this the first
// question anyone asks about a vanished item has no answer anywhere.
//
// That cost real time on the first live run, where a correctly-omitted item and a
// hub that never received the request were indistinguishable from both ends.
func omitted(item transport.Item, reason string) {
	logger.Logger.Debug().
		Str("item", item.ID).
		Str("method", item.Method).
		Str("target", item.Target).
		Bool("has_proof", item.HasProof()).
		Bool("is_bearer", item.IsBearer()).
		Int("params_bytes", len(item.Params)).
		Str("reason", reason).
		Msg("Omitted a private transport item")
}
