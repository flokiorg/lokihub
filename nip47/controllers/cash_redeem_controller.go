package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipIC"
	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	decodepay "github.com/flokiorg/lokihub/decodepay"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/transactions"
)

const (
	// nostrKindClaimProof is a recipient's per-claim proof of identity. Unlike
	// the old kind-35521 "identity declaration" (a static, reusable
	// declaration of connection_key ownership) — and unlike NIP-IC's own
	// kind 35521 (Identity Connection), a genuinely different, similarly
	// long-lived claim this event was never compatible with despite sharing
	// its number — this event is signed fresh for each cash_redeem call and
	// is bound to one specific wallet AND one specific invoice (see
	// verifyClaimIdentityEvent) — a captured/intercepted copy of it is
	// useless for any invoice other than the one it was signed for, which
	// matters here because a cash_wallet's connection is meant to be
	// shared/public, so anyone holding it can decrypt every cash_redeem
	// request sent on it, including other recipients'. Kind 23198 (not
	// 35521): this proof is never independently published to a relay — it
	// only ever travels embedded inside an already end-to-end-encrypted
	// NIP-47 request body — so it belongs in the ephemeral range
	// (20000-29999), not the addressable/parameterized-replaceable range
	// (30000-39999) NIP-IC's long-lived claim correctly uses. 23198 sits
	// directly adjacent to NIP-47's own 23194-23197 block, since this NIP
	// depends on NIP-47.
	nostrKindClaimProof = 23198
	// nostrKindIAAttestation is unchanged from the old design: an Identity
	// Authority's signed attestation that a given nostr pubkey owns a given
	// connection_key. Only used for identity_type == connection_key.
	nostrKindIAAttestation = 35522

	// cashRedeemIdentityFreshnessWindow bounds how old (or how far in the
	// future) a claim proof's own timestamp may be. Defense-in-depth on top
	// of the invoice/wallet binding above — not the primary protection.
	cashRedeemIdentityFreshnessWindow = 5 * time.Minute

	// cashRedeemRateLimitPerHour is the fallback used by tests, which build a
	// config.AppConfig literal directly rather than through envconfig.Process.
	cashRedeemRateLimitPerHour = 20
)

// cash_redeem's request struct is github.com/ohstr/nmilat/nipcash's own
// exported nipcash.CashRedeemRequest — same wire shape, field-for-field
// identical to this controller's former local cashRedeemParams, adopted
// directly instead of maintaining a parallel copy (nmilat migration, PR
// #90). The response stays this package's own payResponse (models.go): it's
// shared across pay_invoice/cash_redeem/other paying methods and carries a
// fee_skim_mloki field nipcash.CashRedeemResult doesn't have, so it isn't a
// clean swap candidate.
func (controller *nip47Controller) HandleCashRedeemEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, tags nostr.Tags) {
	params := &nipcash.CashRedeemRequest{}
	resp := decodeRequest(nip47Request, params)
	if resp != nil {
		publishResponse(resp, tags)
		return
	}

	isBearer := params.BearerSecret != ""

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Str("identity_type", params.IdentityType).
		Bool("bearer", isBearer).
		Msg("Handling cash_redeem request")

	// 1. cash_redeem only ever makes sense against a cash_wallet — reject
	// outright rather than relying solely on scope absence elsewhere.
	if app.Kind != db.AppKindCashWallet {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "cash_redeem requires a cash_wallet app")
		return
	}

	// 2. Rate limit per connection. Since this connection may be shared by
	// several recipients, this throttles the wallet as a whole, not any one
	// caller specifically — intentional, given the connection itself may be
	// widely held. This is also the ONLY throttle standing between a bearer
	// slice and an attacker who's guessing at its secret, since a bearer
	// redemption has no signature to forge — only a secret to guess.
	if !controller.cashClaimLimiter.Allow(app.AppPubkey, controller.cfg.GetEnv().CashWalletClaimRateLimitPerHour) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RATE_LIMITED, "rate limit exceeded for cash_redeem")
		return
	}

	// 3. Basic param validation. A bearer redemption and an identity-bound
	// one are mutually exclusive param shapes, not two optional variants of
	// the same one — mixing them is rejected rather than picking one side to
	// honor.
	var identityType, identityValue string
	if isBearer {
		if params.IdentityType != "" || params.IdentityValue != "" || params.IdentityEvent != "" || params.AttestationEvent != "" {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				"bearer_secret is mutually exclusive with identity_type, identity_value, identity_event, and attestation_event")
			return
		}
		if params.Invoice == "" {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "invoice is required")
			return
		}
		secretBytes, hexErr := hex.DecodeString(params.BearerSecret)
		if hexErr != nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "bearer_secret must be hex")
			return
		}
		hash := sha256.Sum256(secretBytes)
		identityType = db.CashIdentityBearer
		identityValue = hex.EncodeToString(hash[:])
	} else {
		if params.Invoice == "" || params.IdentityType == "" || params.IdentityValue == "" || params.IdentityEvent == "" {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				"invoice, identity_type, identity_value, and identity_event are all required")
			return
		}
		if params.IdentityType != db.CashIdentityPubkey && params.IdentityType != db.CashIdentityConnectionKey {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				fmt.Sprintf("identity_type must be %q or %q", db.CashIdentityPubkey, db.CashIdentityConnectionKey))
			return
		}
		if params.IdentityType == db.CashIdentityConnectionKey && params.AttestationEvent == "" {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				"attestation_event is required when identity_type is connection_key")
			return
		}
		identityType = params.IdentityType
		identityValue = params.IdentityValue
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
	claim, err := controller.appsService.GetCashWalletClaim(app.ID, identityType, identityValue)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to look up Cash wallet claim")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to look up claim")
		return
	}
	if claim == nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_NOT_FOUND,
			controller.noSliceRegisteredMessage(app.ID, identityType, identityValue))
		return
	}

	// 6. Parse and verify the kind-23198 claim proof — identity-bound slices
	// only. A bearer slice's entire proof is the hash-matched lookup in step
	// 5 above: presenting the correct secret is necessary and sufficient, so
	// there is nothing further to verify here.
	var identityEvent nostr.Event
	var attestationEvent nostr.Event
	if !isBearer {
		if err := json.Unmarshal([]byte(params.IdentityEvent), &identityEvent); err != nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "identity_event is not valid JSON")
			return
		}
		walletPubkey := ""
		if app.WalletPubkey != nil {
			walletPubkey = *app.WalletPubkey
		}
		attestationEventID := ""
		if identityType == db.CashIdentityConnectionKey {
			if err := json.Unmarshal([]byte(params.AttestationEvent), &attestationEvent); err != nil {
				respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "attestation_event is not valid JSON")
				return
			}
			attestationEventID = attestationEvent.ID
		}
		if err := verifyClaimIdentityEvent(&identityEvent, identityType, identityValue, walletPubkey, paymentRequest.PaymentHash, attestationEventID); err != nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, err.Error())
			return
		}
	}

	// 7. For connection_key mode, first re-check that the IA recorded on this
	// claim at wallet-creation time is *still* a trusted Identity Authority —
	// checked live, here, rather than only ever at creation time, so revoking
	// a compromised IA immediately blocks future claims it attested instead
	// of leaving them honorable until their own attestation expiry lapses.
	if identityType == db.CashIdentityConnectionKey {
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
		if err := verifyClaimAttestationEvent(&attestationEvent, claim.IAPubkey, identityEvent.PubKey, identityValue); err != nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, err.Error())
			return
		}
	}

	// 8. Atomically claim the slice — guards the actual payout against races
	// and replays. RowsAffected==0 here means a concurrent redeem/transfer won
	// since step 5's lookup. Reported with a distinct message from step 5's
	// "never existed" case — list_recipients already discloses claimed/
	// claimed_at to any holder of this shared connection, so naming "already
	// redeemed" here protects nothing that isn't already visible one method
	// over, while sparing a legitimate recipient who was in fact paid from
	// seeing a message that reads as "you were never owed anything."
	claimedAmount, err := controller.appsService.ClaimCashSlice(app.ID, identityType, identityValue)
	if err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_NOT_FOUND, "this slice has already been redeemed")
		return
	}

	// 9. The "not partially, in one shot" rule as an explicit, direct check:
	// the invoice's resolved amount must equal the slice's own net
	// redeemable amount exactly — the full slice for a redemption that will
	// resolve to a same-node payment (no fee applies), or the slice minus
	// this claim's own RedeemFeePpm cut for a genuine external one. Peeking
	// at transactions.IsSelfPayment here — the SAME predicate
	// SendPaymentSync itself evaluates moments later — lets the recipient's
	// invoice be checked against the RIGHT expected amount before payment is
	// even attempted, rather than discovering after the fact that they built
	// it for the wrong one. See list_recipients (redeem_fee_millis/
	// net_redeemable_millis) for the quote a client should build this invoice
	// from in the first place.
	resolvedAmount := uint64(paymentRequest.AmountMloki) //nolint:gosec // mloki amounts are always far below int64/uint64 range
	if resolvedAmount == 0 && params.Amount != nil {
		resolvedAmount = *params.Amount
	}
	willBeSelfPayment := transactions.IsSelfPayment(controller.db, paymentRequest, controller.lnClient)
	hubFeeMloki := uint64(0)
	if !willBeSelfPayment {
		hubFeeMloki = transactions.CalculateFeeSkimMloki(uint64(claimedAmount), claim.RedeemFeePpm) //nolint:gosec // claimedAmount is always non-negative
	}
	expectedAmount := uint64(claimedAmount) - hubFeeMloki //nolint:gosec // claimedAmount is always non-negative and >= hubFeeMloki (a <=100% cut of it)
	if resolvedAmount != expectedAmount {
		if unclaimErr := controller.appsService.UnclaimCashSlice(app.ID, identityType, identityValue); unclaimErr != nil {
			logger.Logger.Error().Err(unclaimErr).Uint("app_id", app.ID).Msg("Failed to roll back Cash wallet slice claim after amount mismatch")
		}
		// The generic message below is correct on its own, but a recipient
		// who followed list_recipients' own advice (build the invoice for
		// net_redeemable_millis, the WORST-CASE quote) and presented exactly
		// that fee-reduced amount, only to have this specific redemption
		// resolve same-node (fee-free, full amount required), would read
		// "net redeemable amount of X" as agreeing with the very value that
		// just got rejected — same phrase, different number, no indication
		// why. Name the mechanism explicitly for that specific case.
		message := fmt.Sprintf("invoice amount %d does not exactly match your net redeemable amount of %d millis (allocated share %d minus redeem fee %d)",
			resolvedAmount, expectedAmount, claimedAmount, hubFeeMloki)
		if willBeSelfPayment && resolvedAmount < uint64(claimedAmount) { //nolint:gosec // claimedAmount is always non-negative
			message = fmt.Sprintf("invoice amount %d does not match: this redemption resolves to a same-node payment, which is always fee-free — present an invoice for the full %d instead of a fee-reduced quote",
				resolvedAmount, claimedAmount)
		}
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, message)
		return
	}

	// 10. Pay. Build outgoing metadata from caller input, stripping any
	// caller-supplied internal_transfer/cash_claim_slice/cash_redeem_fee_mloki
	// keys (spoofing prevention — mirrors pay_invoice_controller.go's
	// internal_transfer stripping) before setting cash_claim_slice ourselves,
	// which bypasses enforceCashFullDrain's whole-wallet-balance check: that
	// check is wrong for a shared wallet (it would reject a recipient's
	// payout whenever other recipients' unclaimed slices are still sitting
	// in the same balance), and step 9 above already enforces the correct,
	// per-slice exact-amount rule in its place. cash_redeem_fee_mloki is the
	// quoted hub fee, computed above from this claim's own RedeemFeePpm —
	// threaded through so markTransactionSettled can reconcile it against
	// the real routing fee at settlement (transactions.reconcileCashRedeemFee).
	metadata := map[string]interface{}{}
	// (cash_redeem has no metadata param of its own in the wire format above;
	// reserved for parity with pay_invoice's shape/future extension.)
	delete(metadata, "internal_transfer")
	delete(metadata, "cash_claim_slice")
	delete(metadata, "cash_redeem_fee_mloki")
	metadata["cash_claim_slice"] = true
	metadata["cash_redeem_fee_mloki"] = hubFeeMloki

	transaction, err := controller.transactionsService.SendPaymentSync(bolt11, params.Amount, metadata, controller.lnClient, &app.ID, &requestEventId)
	if err != nil {
		if unclaimErr := controller.appsService.UnclaimCashSlice(app.ID, identityType, identityValue); unclaimErr != nil {
			logger.Logger.Error().Err(unclaimErr).Uint("app_id", app.ID).Msg("Failed to roll back Cash wallet slice claim after payment failure")
		}
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to pay cash_redeem invoice")
		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Error:      mapNip47Error(err),
		}, tags)
		return
	}

	if transaction == nil || transaction.Preimage == nil {
		logger.Logger.Error().Uint("app_id", app.ID).Msg("cash_redeem payment succeeded but transaction or preimage is nil")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "payment completed but preimage unavailable")
		return
	}

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Str("identity_type", identityType).
		Uint64("amount_mloki", resolvedAmount).
		Msg("Cash wallet slice claimed")

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result: payResponse{
			Preimage: *transaction.Preimage,
			// FeesPaid is the hub's own redeem fee (what the recipient
			// actually bore, already deducted from their invoice amount in
			// step 9) — NOT transaction.FeeMloki, the real Lightning routing
			// fee, which under this design is never charged to the
			// recipient (see transactions.reconcileCashRedeemFee).
			FeesPaid: hubFeeMloki,
		},
	}, tags)
}

// verifyClaimIdentityEvent checks a kind-23198 claim proof: valid signature;
// bound to this exact wallet (d-tag) and this exact invoice (bolt11_hash
// tag) — the binding that makes an intercepted proof unusable for any
// invoice other than the one it was created for, which matters on a shared/
// public connection where anyone holding it can decrypt every cash_redeem
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
	// single-use guarantee comes from ClaimCashSlice's atomic claim, and
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
	if evTime.Before(now.Add(-cashRedeemIdentityFreshnessWindow)) || evTime.After(now.Add(time.Minute)) {
		return fmt.Errorf("identity_event is stale or has a future timestamp")
	}

	if identityType == db.CashIdentityConnectionKey {
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
// pubkey — identity_event's own signer), carries a valid, unexpired
// expiration tag (NIP-40), and carries the platform/evidence tags NIP-IC's
// own Attestation shape requires.
//
// Structural parsing (kind, signature, event-ID integrity, required tag
// presence, evidence-JSON-object validity) is delegated to
// nipIC.ParseAttestation — the canonical parser for NIP-IC's own event
// shape, the same one nmilat's own nipcash.BySigningConnectionKey uses
// client-side. This is part of the nmilat migration (PR #90); equivalence
// with this codebase's previous hand-rolled check was verified first, in
// nip47/controllers/nmilat_attestation_equivalence_test.go, before this
// swap. Two previously-inert gaps are deliberately closed by adopting it,
// for the same reason the other two proof-verification functions
// (verifyClaimIdentityEvent, verifyCircleWalletIdentityEvent) already apply
// them:
//
//  1. Event ID integrity is now checked (nip01.Event.Verify(), inside
//     ParseAttestation, recomputes the ID from content and compares it to
//     the stored field). This function previously never checked that,
//     unlike its two siblings, on the reasoning that the attestation's ID
//     isn't used as a trust anchor anywhere in this codebase (the claim
//     proof's e-tag reference to it is a citation, not a security
//     binding) — that reasoning still holds, but the check is free (a hash
//     comparison already computed while parsing), so there's no reason to
//     keep depending on it by hand.
//  2. The evidence tag must now be a JSON *object* matching NIP-IC's own
//     evidence shape, not merely any syntactically valid JSON value — a
//     bare JSON string/number/array previously passed this function's
//     plain json.Valid check. Evidence content still isn't interpreted for
//     any decision here, so this only rejects a wider set of malformed
//     input, nothing behavioral.
//
// The expiration tag stays mandatory-and-unexpired here even though
// nipIC.ParseAttestation itself treats expiration as optional (it has real
// per-attestation revocation, via a NIP-09 kind-5 deletion, to fall back on
// instead): this codebase's trust model only supports revoking an Identity
// Authority as a whole (apps.IdentityAuthorityManager.IsTrusted — checked by
// HandleCashRedeemEvent, step 7, right before this function runs), so
// expiration is the only bound on how long a not-yet-revoked-but-later-to-
// be-revoked attestation stays honorable — see NIP-CASH.md's own Security
// Considerations for the same reasoning at the spec level.
func verifyClaimAttestationEvent(ev *nostr.Event, iaPubkey, nostrPubkey, connectionKey string) error {
	nip01Ev, err := toNip01Event(ev)
	if err != nil {
		return fmt.Errorf("attestation_event: %w", err)
	}
	attestation, err := nipIC.ParseAttestation(nip01Ev)
	if err != nil {
		return fmt.Errorf("attestation_event: %w", err)
	}
	if attestation.PubKey != iaPubkey {
		return fmt.Errorf("attestation_event must be signed by the trusted ia_pubkey recorded for this slice")
	}
	if string(attestation.ConnectionKey) != connectionKey {
		return fmt.Errorf("attestation_event d-tag does not match connection_key")
	}
	if attestation.UserPubkey != nostrPubkey {
		return fmt.Errorf("attestation_event p-tag does not match the claimant's nostr pubkey")
	}
	if attestation.ExpiresAt == nil {
		return fmt.Errorf("attestation_event is missing a required expiration tag")
	}
	if time.Now().After(*attestation.ExpiresAt) {
		return fmt.Errorf("attestation_event has expired")
	}
	return nil
}

// toNip01Event converts a go-nostr Event into nmilat's own nip01.Event via a
// plain JSON round-trip. Both packages use the identical NIP-01 wire field
// names (id/pubkey/created_at/kind/tags/content/sig), so this is a lossless
// conversion between two representations of the same wire format, not a
// reinterpretation of it.
func toNip01Event(ev *nostr.Event) (*nip01.Event, error) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("marshal event: %w", err)
	}
	var out nip01.Event
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unmarshal event: %w", err)
	}
	return &out, nil
}
