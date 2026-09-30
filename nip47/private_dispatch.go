package nip47

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"gorm.io/gorm"

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
		omitted(item, "method is not on the private-transport allowlist")
		return transport.Result{}, false
	}

	// 2. Verify the bill proof's SIGNATURE first, before the bill is looked up.
	//
	// The verification is bill-independent and is the expensive step, so doing it here
	// makes a missing bill and a foreign proof cost the same — see billProofSigner for
	// the timing oracle this closes. Who the signer must BE is decided after the
	// lookup, because the answer differs for a live bill and a tombstoned one.
	signer, signed := svc.billProofSigner(item, binding)
	if !signed {
		omitted(item, "bill proof missing, malformed, or not verifiable")
		return transport.Result{}, false
	}

	// 3. Resolve the bill by wallet_pubkey ALONE. There is no app_pubkey to use — the
	// envelope's outer key is ephemeral — which is why apps.wallet_pubkey carries its own
	// index; the composite one leads with app_pubkey and cannot serve this.
	var app db.App
	if err := svc.db.Where("wallet_pubkey = ?", item.Target).First(&app).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Logger.Error().Err(err).Msg("Failed to resolve a private item's target")
		}
		// No live bill. That is usually a wallet pubkey this hub never served, and the
		// answer is silence — but a bill this hub DESTROYED also lands here, and its
		// holder is entitled to a definitive "spent" for a bounded window. The
		// standard transport has answered that since tombstones existed; this is the
		// same answer, reached through the bill proof instead of through a request
		// encrypted to the bill.
		if result, ok := svc.tryPrivateSpentBill(item, binding, signer); ok {
			return result, true
		}
		omitted(item, "no bill with this target wallet_pubkey")
		return transport.Result{}, false
	}

	// 2.5. Prove the sender HOLDS this bill, before anything else about it is
	// considered.
	//
	// This is the gate the standard transport got for free: a request there was
	// encrypted to the bill's own wallet pubkey, so sending one at all demonstrated
	// possession. An envelope is encrypted to the hub's inbox instead and merely
	// NAMES a target, and a wallet pubkey is public, so without this gate anyone
	// could ask about any bill they could name.
	//
	// It is what makes every answer below safe to give. Once the sender has proved
	// possession, confirming the bill exists — by declining, or by tombstoning it —
	// tells them nothing they did not already know. Failing it is an OMISSION, so a
	// caller who cannot prove possession learns nothing at all, which is what keeps
	// a guessed wallet pubkey from becoming an existence oracle.
	if !billProofHolder(signer, app.AppPubkey) {
		omitted(item, "bill proof not signed by this bill's connection key")
		return transport.Result{}, false
	}

	// 2.6. An item MUST NOT carry BOTH a slice proof and a cash secret
	// (NIP-CASH §Bearer Items). They authorize differently, and an item asserting both
	// leaves this hub to choose — either choice hiding the sender's mistake.
	//
	// Checked at the TRANSPORT, because the requirement is about the item. The money
	// methods do reject the mixed PARAM shape themselves (cash_redeem step 3), but
	// cash_status's params carry no identity fields at all, so nothing downstream can see
	// the conflict: the proof is on the envelope and the secret is in params. That the
	// stray secret is currently harmless there is an accident of which fields each method
	// happens to carry, not a property anyone arranged — exactly what breaks when a field
	// is added.
	//
	// An error rather than an omission, and placed after the possession gate so it is safe
	// to be informative: the sender has proved they hold this bill, so naming their mistake
	// discloses nothing, and an omission would leave a malformed client with no way to
	// learn why it is being ignored.
	if item.HasProof() && item.IsBearer() {
		return transport.Result{
			ID:         item.ID,
			ResultType: item.Method,
			Error: &transport.ResultError{
				Code:    constants.ERROR_BAD_REQUEST,
				Message: "an item must carry either a slice proof or a cash secret, never both",
			},
		}, true
	}

	// 3. Authorize the ITEM. The envelope proves nothing about who may act on a bill, since
	// its author is a throwaway key.
	//
	// The authorized identity comes back rather than being discarded, because cash_status
	// needs it: scoping the roster to "mine" MUST be decided from the proof's own signer and
	// never from anything the item asserts about itself (NIP-CASH §Scoping the Roster). An
	// item that could name its own scope subject could read a co-recipient's row by naming
	// theirs.
	signer, billIsCashMode, authorized := svc.privateItemIsAuthorized(item, binding, &app)
	if !authorized {
		// An ERROR now, not an omission, and only because gate 2.5 passed: the sender
		// has proved they hold this bill, so telling them they hold no SLICE of it
		// discloses nothing further — they could have learned as much by trying to
		// spend it.
		//
		// This used to be an omission, which was correct while possession was
		// unprovable but cost real information: "this bill is not addressed to you"
		// became indistinguishable from "the hub is down", and clients reported it as
		// retryable, so they retried a token that would never be theirs.
		//
		// Two distinguishable reasons, both safe to state now, and worth separating
		// because they call for different actions: a caller missing a slice proof has
		// built the item wrong, while one whose signer holds no slice is holding a
		// bill that simply is not addressed to them.
		code, message := constants.ERROR_NOT_FOUND,
			"no slice of this bill is registered for the identity that signed this item"
		if !item.HasProof() && !item.IsBearer() {
			code, message = constants.ERROR_BAD_REQUEST,
				"this item carries no slice proof, and this bill is identity-bound"
		}
		return transport.Result{
			ID:         item.ID,
			ResultType: item.Method,
			Error:      &transport.ResultError{Code: code, Message: message},
		}, true
	}

	// 4. Scope, checked per item. Batching MUST NOT be a way around permissions.
	scope, err := permissions.RequestMethodToScope(item.Method)
	if err != nil {
		omitted(item, "method maps to no permission scope")
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
		omitted(item, "could not record the request event")
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
	case constants.NIP47MethodCashStatus:
		controller.HandleCashStatusEvent(ctx, request, requestEvent.ID, &app, collector.publish,
			&controllers.CashStatusCaller{IdentityValue: signer, IsCash: billIsCashMode})
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
// Only cash_status is answered. A cash_redeem or cash_transfer naming a destroyed
// bill keeps its silence, exactly as on the standard transport: the agreed design is
// three outcomes on the STATUS method, and answering the others would return a
// result_type that does not match the request.
func (svc *nip47Service) tryPrivateSpentBill(item transport.Item, binding PrivateItemBinding, signer string) (transport.Result, bool) {
	if item.Method != nipcash.MethodCashStatus {
		return transport.Result{}, false
	}

	// Gate 2, using db.RetentionWindowOpen rather than a local comparison so this
	// path and the relay gate agree exactly at the boundary — they did not when each
	// wrote the comparison itself.
	retainedUntil, ok := db.SpentBillRetainedUntil(svc.db, item.Target)
	if !ok || !db.RetentionWindowOpen(retainedUntil, time.Now()) {
		return transport.Result{}, false
	}

	var bill db.CashBillArchive
	if err := svc.db.Where("wallet_pubkey = ?", item.Target).First(&bill).Error; err != nil {
		return transport.Result{}, false
	}

	// Gate 3.
	pairingKey, err := svc.keys.GetCashPairingKey(bill.WalletAppID)
	if err != nil {
		logger.Logger.Error().Err(err).
			Uint("walletAppId", bill.WalletAppID).
			Msg("Failed to derive a spent bill's pairing key")
		return transport.Result{}, false
	}
	expectedPubkey, err := nostr.GetPublicKey(pairingKey)
	if err != nil {
		return transport.Result{}, false
	}
	if !billProofHolder(signer, expectedPubkey) {
		return transport.Result{}, false
	}

	deadline := retainedUntil.Unix()
	body, err := json.Marshal(nipcash.CashStatusResult{
		Error:         nipcash.ErrorSpent,
		RetainedUntil: &deadline,
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
// the same pubkey is re-derived from the archive; see tryPrivateSpentBill.
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

// billProofHolder reports whether an already-verified signer is the key this bill's
// proof must carry. Free: the expensive work happened in billProofSigner.
func billProofHolder(signer, wantPubkey string) bool {
	return wantPubkey != "" && signer == wantPubkey
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
func (svc *nip47Service) privateItemIsAuthorized(
	item transport.Item,
	binding PrivateItemBinding,
	app *db.App,
) (signer string, billIsCashMode bool, authorized bool) {
	// create_circle_wallet acts on a HUB rather than a bill, so there are no claims to
	// match a signer against — the kind-23199 identity proof inside its own params is what
	// authorizes it, and the controller checks that. Handled first because a hub
	// legitimately has no claims at all.
	isCircleJoin := item.Method == constants.NIP47MethodCreateCircleWallet

	claims, err := svc.appsService.ListClaimsForWallet(app.ID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to read a bill's claims")
		return "", false, false
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
		return "", billIsCashMode, billIsCashMode
	}

	paramsHash, err := transport.CanonicalParamsHash(item.Params)
	if err != nil {
		return "", billIsCashMode, false
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
		return "", billIsCashMode, false
	}
	if isCircleJoin {
		return signer, billIsCashMode, true
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
		return signer, billIsCashMode, true
	}

	decidableHere := true
	for _, claim := range claims {
		if claim.IdentityType != db.CashIdentityCash && claim.IdentityType != db.CashIdentityPubkey {
			decidableHere = false
			break
		}
	}
	// Deferring is only safe for a method whose controller actually authorizes. That is the
	// premise the comment above rests on, and it does not hold for cash_status:
	// HandleCashStatusEvent performs no identity check at all — it filters the roster by
	// scope and answers. So for cash_status there is nothing behind this gate, and deferring
	// meant not checking at all.
	//
	// The cost of getting that wrong: on a bill carrying any connection_key recipient, mere
	// POSSESSION of the token (a valid kind-23193 bill proof, signed with a pairing secret
	// that is app-ID-derived and unrotatable) read the WHOLE roster — every recipient's
	// identity_value and amount — with scope "all", while the same caller correctly got
	// NOT_FOUND on an all-pubkey bill. §Scoping the Roster makes NOT_FOUND a MUST for a
	// no-slice caller, unconditional on identity mode. The parties who hold a token but no
	// slice are real: a recipient removed by DeleteCashClaim, a recipient who transferred her
	// slice away in place, and anyone the widely-held connection was ever shown to.
	//
	// So the deferral is now scoped to the methods it was reasoned about. cash_status falls
	// through to the decidable check below, which means a connection_key recipient cannot
	// read their own row over cash_status — their identity_value is a hash, and cash_status
	// carries no attestation to bind it to a signer. That is a real functional loss, taken
	// deliberately: the alternative is handing every roster to every token holder. Restoring
	// it properly needs cash_status to accept an IA attestation the way cash_redeem does,
	// which is a protocol addition rather than a gate fix.
	if !decidableHere && item.Method != constants.NIP47MethodCashStatus {
		return signer, billIsCashMode, true
	}
	for _, claim := range claims {
		if claim.IdentityType != db.CashIdentityCash && claim.IdentityValue == signer {
			return signer, billIsCashMode, true
		}
	}
	return "", billIsCashMode, false
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
