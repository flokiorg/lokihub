package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	decodepay "github.com/flokiorg/lokihub/decodepay"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
)

const (
	// nostrKindClaimProof is a recipient's per-claim proof of identity. Unlike
	// the old kind-35521 "identity declaration" (a static, reusable
	// declaration of connection_key ownership), this event is signed fresh for
	// each claim_funds call and is bound to one specific wallet AND one
	// specific invoice (see verifyClaimIdentityEvent) — a captured/intercepted
	// copy of it is useless for any invoice other than the one it was signed
	// for, which matters here because a jit_wallet's connection is meant to be
	// shared/public, so anyone holding it can decrypt every claim_funds
	// request sent on it, including other recipients'.
	nostrKindClaimProof = 35521
	// nostrKindIAAttestation is unchanged from the old design: an Identity
	// Authority's signed attestation that a given nostr pubkey owns a given
	// connection_key. Only used for identity_type == connection_key.
	nostrKindIAAttestation = 35522

	// jitClaimIdentityFreshnessWindow bounds how old (or how far in the
	// future) a claim proof's own timestamp may be. Defense-in-depth on top
	// of the invoice/wallet binding above — not the primary protection.
	jitClaimIdentityFreshnessWindow = 5 * time.Minute

	// jitClaimRateLimitPerHour is the fallback used by tests, which build a
	// config.AppConfig literal directly rather than through envconfig.Process.
	jitClaimRateLimitPerHour = 20
)

type claimFundsParams struct {
	Invoice       string  `json:"invoice"`
	Amount        *uint64 `json:"amount,omitempty"` // override for amountless invoices, mirrors pay_invoice
	IdentityType  string  `json:"identity_type"`    // "pubkey" | "connection_key"
	IdentityValue string  `json:"identity_value"`
	// IdentityEvent is the JSON-encoded kind-35521 claim proof, signed fresh
	// for this call and bound to this wallet + this invoice.
	IdentityEvent string `json:"identity_event"`
	// AttestationEvent is the JSON-encoded kind-35522 IA attestation, required
	// only when identity_type == connection_key.
	AttestationEvent string `json:"attestation_event,omitempty"`
}

func (controller *nip47Controller) HandleClaimFundsEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, tags nostr.Tags) {
	params := &claimFundsParams{}
	resp := decodeRequest(nip47Request, params)
	if resp != nil {
		publishResponse(resp, tags)
		return
	}

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Str("identity_type", params.IdentityType).
		Msg("Handling claim_funds request")

	// 1. claim_funds only ever makes sense against a jit_wallet — reject
	// outright rather than relying solely on scope absence elsewhere.
	if app.Kind != db.AppKindJITWallet {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "claim_funds requires a jit_wallet app")
		return
	}

	// 2. Rate limit per connection. Since this connection may be shared by
	// several recipients, this throttles the wallet as a whole, not any one
	// caller specifically — intentional, given the connection itself may be
	// widely held.
	if !controller.jitClaimLimiter.Allow(app.AppPubkey, controller.cfg.GetEnv().JITWalletClaimRateLimitPerHour) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RATE_LIMITED, "rate limit exceeded for claim_funds")
		return
	}

	// 3. Basic param validation.
	if params.Invoice == "" || params.IdentityType == "" || params.IdentityValue == "" || params.IdentityEvent == "" {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			"invoice, identity_type, identity_value, and identity_event are all required")
		return
	}
	if params.IdentityType != db.JITAllocIdentityPubkey && params.IdentityType != db.JITAllocIdentityConnectionKey {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			fmt.Sprintf("identity_type must be %q or %q", db.JITAllocIdentityPubkey, db.JITAllocIdentityConnectionKey))
		return
	}
	if params.IdentityType == db.JITAllocIdentityConnectionKey && params.AttestationEvent == "" {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			"attestation_event is required when identity_type is connection_key")
		return
	}

	// 4. Decode the invoice up front — the identity proof must bind to it.
	bolt11 := strings.ToLower(params.Invoice)
	paymentRequest, err := decodepay.Decode(bolt11)
	if err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			fmt.Sprintf("Failed to decode bolt11 invoice: %s", err.Error()))
		return
	}

	// 5. Read-only lookup of the claimed slice BEFORE touching the atomic
	// claim guard, so a proof that fails verification never briefly occupies
	// (and can never grief) the slot a legitimate concurrent claimer needs.
	claim, err := controller.appsService.GetJITWalletClaim(app.ID, params.IdentityType, params.IdentityValue)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to look up JIT wallet claim")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to look up claim")
		return
	}
	if claim == nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_NOT_FOUND, "no unclaimed slice for this identity")
		return
	}

	// 6. Parse and verify the kind-35521 claim proof.
	var identityEvent nostr.Event
	if err := json.Unmarshal([]byte(params.IdentityEvent), &identityEvent); err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "identity_event is not valid JSON")
		return
	}
	walletPubkey := ""
	if app.WalletPubkey != nil {
		walletPubkey = *app.WalletPubkey
	}
	var attestationEvent nostr.Event
	attestationEventID := ""
	if params.IdentityType == db.JITAllocIdentityConnectionKey {
		if err := json.Unmarshal([]byte(params.AttestationEvent), &attestationEvent); err != nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "attestation_event is not valid JSON")
			return
		}
		attestationEventID = attestationEvent.ID
	}
	if err := verifyClaimIdentityEvent(&identityEvent, params.IdentityType, params.IdentityValue, walletPubkey, paymentRequest.PaymentHash, attestationEventID); err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, err.Error())
		return
	}

	// 7. For connection_key mode, first re-check that the IA recorded on this
	// claim at wallet-creation time is *still* a trusted Identity Authority —
	// checked live, here, rather than only ever at creation time, so revoking
	// a compromised IA immediately blocks future claims it attested instead
	// of leaving them honorable until their own attestation expiry lapses.
	if params.IdentityType == db.JITAllocIdentityConnectionKey {
		trusted, err := controller.iaChecker.IsTrusted(claim.IAPubkey)
		if err != nil {
			logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to check Identity Authority trust")
			respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to check Identity Authority trust")
			return
		}
		if !trusted {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "the Identity Authority for this claim has been revoked")
			return
		}
		// Also verify the IA attestation itself: signature, connection_key/
		// claimant tag binding, and its own expiration.
		if err := verifyClaimAttestationEvent(&attestationEvent, claim.IAPubkey, identityEvent.PubKey, params.IdentityValue); err != nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, err.Error())
			return
		}
	}

	// 8. Atomically claim the slice — guards the actual payout against races
	// and replays. RowsAffected==0 here (a concurrent claim won since step 5)
	// is reported identically to "not found" for the same reason step 5 is.
	claimedAmount, err := controller.appsService.ClaimJITWalletSlice(app.ID, params.IdentityType, params.IdentityValue)
	if err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_NOT_FOUND, "no unclaimed slice for this identity")
		return
	}

	// 9. The "not partially, in one shot" rule as an explicit, direct check:
	// the invoice's resolved amount must equal the slice exactly. Mirrors
	// SendPaymentSync's own amount resolution (invoice's own MSat, or the
	// caller's override only for a zero-amount invoice).
	resolvedAmount := uint64(paymentRequest.MSat)
	if resolvedAmount == 0 && params.Amount != nil {
		resolvedAmount = *params.Amount
	}
	if resolvedAmount != uint64(claimedAmount) {
		if unclaimErr := controller.appsService.UnclaimJITWalletSlice(app.ID, params.IdentityType, params.IdentityValue); unclaimErr != nil {
			logger.Logger.Error().Err(unclaimErr).Uint("app_id", app.ID).Msg("Failed to roll back JIT wallet slice claim after amount mismatch")
		}
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			fmt.Sprintf("invoice amount %d does not exactly match your allocated share of %d mloki", resolvedAmount, claimedAmount))
		return
	}

	// 10. Pay. Build outgoing metadata from caller input, stripping any
	// caller-supplied internal_transfer/jit_claim_slice keys (spoofing
	// prevention — mirrors pay_invoice_controller.go's internal_transfer
	// stripping) before setting jit_claim_slice ourselves, which bypasses
	// enforceJITFullDrain's whole-wallet-balance check: that check is wrong
	// for a shared wallet (it would reject a recipient's payout whenever
	// other recipients' unclaimed slices are still sitting in the same
	// balance), and step 9 above already enforces the correct, per-slice
	// exact-amount rule in its place.
	metadata := map[string]interface{}{}
	// (claim_funds has no metadata param of its own in the wire format above;
	// reserved for parity with pay_invoice's shape/future extension.)
	delete(metadata, "internal_transfer")
	delete(metadata, "jit_claim_slice")
	metadata["jit_claim_slice"] = true

	transaction, err := controller.transactionsService.SendPaymentSync(bolt11, params.Amount, metadata, controller.lnClient, &app.ID, &requestEventId)
	if err != nil {
		if unclaimErr := controller.appsService.UnclaimJITWalletSlice(app.ID, params.IdentityType, params.IdentityValue); unclaimErr != nil {
			logger.Logger.Error().Err(unclaimErr).Uint("app_id", app.ID).Msg("Failed to roll back JIT wallet slice claim after payment failure")
		}
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to pay claim_funds invoice")
		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Error:      mapNip47Error(err),
		}, tags)
		return
	}

	if transaction == nil || transaction.Preimage == nil {
		logger.Logger.Error().Uint("app_id", app.ID).Msg("claim_funds payment succeeded but transaction or preimage is nil")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "payment completed but preimage unavailable")
		return
	}

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Str("identity_type", params.IdentityType).
		Uint64("amount_mloki", resolvedAmount).
		Msg("JIT wallet slice claimed")

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result: payResponse{
			Preimage: *transaction.Preimage,
			FeesPaid: transaction.FeeMloki,
		},
	}, tags)
}

// verifyClaimIdentityEvent checks a kind-35521 claim proof: valid signature;
// bound to this exact wallet (d-tag) and this exact invoice (bolt11_hash
// tag) — the binding that makes an intercepted proof unusable for any
// invoice other than the one it was created for, which matters on a shared/
// public connection where anyone holding it can decrypt every claim_funds
// request; a recency window as defense-in-depth; and, depending on mode,
// either self-proof (pubkey) or a reference to the accompanying IA
// attestation (connection_key).
func verifyClaimIdentityEvent(ev *nostr.Event, identityType, identityValue, walletPubkey, invoicePaymentHash, attestationEventID string) error {
	if ev.Kind != nostrKindClaimProof {
		return fmt.Errorf("identity_event must be kind %d, got %d", nostrKindClaimProof, ev.Kind)
	}
	valid, err := ev.CheckSignature()
	if err != nil || !valid {
		return fmt.Errorf("identity_event has invalid signature")
	}
	// CheckSignature verifies the signature against a hash it recomputes from
	// the event's own fields — it does not check that the client-supplied
	// evt.ID matches that hash (only CheckID does). Nothing here currently
	// trusts identityEvent.ID as a security-relevant key (this claim's
	// single-use guarantee comes from ClaimJITWalletSlice's atomic claim, and
	// replay is bound to a specific invoice via bolt11_hash below, not the
	// event ID) — this check is defense in depth / NIP-01 correctness, kept
	// consistent with the sibling verifyCircleWalletIdentityEvent, which does
	// rely on the ID for its own replay guard.
	if !ev.CheckID() {
		return fmt.Errorf("identity_event id does not match its content")
	}
	dTag := ev.Tags.Find("d")
	if len(dTag) < 2 || dTag[1] != walletPubkey {
		return fmt.Errorf("identity_event d-tag does not match this wallet")
	}
	hashTag := ev.Tags.Find("bolt11_hash")
	if len(hashTag) < 2 || hashTag[1] != invoicePaymentHash {
		return fmt.Errorf("identity_event is not bound to this invoice")
	}
	now := time.Now()
	evTime := ev.CreatedAt.Time()
	if evTime.Before(now.Add(-jitClaimIdentityFreshnessWindow)) || evTime.After(now.Add(time.Minute)) {
		return fmt.Errorf("identity_event is stale or has a future timestamp")
	}

	if identityType == db.JITAllocIdentityConnectionKey {
		connKeyTag := ev.Tags.Find("connection_key")
		if len(connKeyTag) < 2 || connKeyTag[1] != identityValue {
			return fmt.Errorf("identity_event connection_key tag does not match identity_value")
		}
		eTag := ev.Tags.FindWithValue("e", attestationEventID)
		if len(eTag) == 0 {
			return fmt.Errorf("identity_event must reference the attestation event via an e-tag")
		}
		return nil
	}

	// pubkey mode: the event's own signer IS the proof of ownership.
	if ev.PubKey != identityValue {
		return fmt.Errorf("identity_event must be signed by identity_value")
	}
	return nil
}

// verifyClaimAttestationEvent checks a kind-35522 event is validly signed by
// iaPubkey (the IA recorded on this slice at wallet-creation time), has the
// correct d-tag (connectionKey) and p-tag (the claimant's real nostr
// pubkey — identity_event's own signer), and carries a valid, unexpired
// expiration tag (NIP-40).
//
// The expiration tag is mandatory here, not merely checked-if-present: this
// codebase's trust model only supports revoking an Identity Authority as a
// whole (apps.IdentityAuthorityManager.IsTrusted — called by
// HandleClaimFundsEvent, step 7, right before this function runs, so a
// revoked IA is rejected before its attestation's own tags are even
// examined) — unlike the wider IA attestation protocol this event shape
// is drawn from, there is no per-attestation revocation (no NIP-09 kind-5
// deletion check against the issuing relay). A single mistaken or
// compromised attestation can't be individually revoked; between IA
// revocation and its own expiration, expiration is what bounds how long a
// not-yet-revoked-but-later-to-be-revoked attestation stays honorable. An
// attestation with no expiration at all (or one that fails to parse)
// would never lapse, permanently short-circuiting that safety net, so
// it's rejected rather than treated as eternally valid.
//
// Like verifyClaimIdentityEvent's own ev.ID, this never calls ev.CheckID(): the
// claim proof's e-tag (checked by the caller before this runs) only has to
// match whatever string the caller also put in this event's client-supplied
// ID field, which isn't independently tied to this event's real content hash.
// That's fine — the ID isn't a trust anchor here, it's just a citation. Every
// actual security property (IA signature over the recomputed content hash,
// the d/p-tag binding to connectionKey and the claimant, and expiration) is
// checked directly below against this event's real signed fields, so a
// mismatched or fabricated ID can't be used to satisfy any of them.
func verifyClaimAttestationEvent(ev *nostr.Event, iaPubkey, nostrPubkey, connectionKey string) error {
	if ev.Kind != nostrKindIAAttestation {
		return fmt.Errorf("attestation_event must be kind %d, got %d", nostrKindIAAttestation, ev.Kind)
	}
	if ev.PubKey != iaPubkey {
		return fmt.Errorf("attestation_event must be signed by the trusted ia_pubkey recorded for this slice")
	}
	valid, err := ev.CheckSignature()
	if err != nil || !valid {
		return fmt.Errorf("attestation_event has invalid signature")
	}
	dTag := ev.Tags.Find("d")
	if len(dTag) < 2 || dTag[1] != connectionKey {
		return fmt.Errorf("attestation_event d-tag does not match connection_key")
	}
	pTag := ev.Tags.Find("p")
	if len(pTag) < 2 || pTag[1] != nostrPubkey {
		return fmt.Errorf("attestation_event p-tag does not match the claimant's nostr pubkey")
	}
	expTag := ev.Tags.Find("expiration")
	if len(expTag) < 2 {
		return fmt.Errorf("attestation_event is missing a required expiration tag")
	}
	expUnix, parseErr := strconv.ParseInt(expTag[1], 10, 64)
	if parseErr != nil {
		return fmt.Errorf("attestation_event has a malformed expiration tag")
	}
	if time.Now().Unix() > expUnix {
		return fmt.Errorf("attestation_event has expired")
	}
	return nil
}
