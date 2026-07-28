package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/jitwallet"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
)

type jitTransferNewIdentityParam struct {
	IdentityType  string `json:"identity_type"` // "pubkey" | "connection_key" | "bearer"
	IdentityValue string `json:"identity_value,omitempty"`
	IAPubkey      string `json:"ia_pubkey,omitempty"` // required iff identity_type == connection_key
}

type jitTransferParams struct {
	// The caller's CURRENT registered identity — mutually exclusive with
	// BearerSecret. IdentityEvent is the JSON-encoded kind-35521 transfer
	// proof, signed fresh for this call and bound to this wallet + this
	// specific new_identity (see verifyTransferIdentityEvent).
	IdentityType     string `json:"identity_type,omitempty"`
	IdentityValue    string `json:"identity_value,omitempty"`
	IdentityEvent    string `json:"identity_event,omitempty"`
	AttestationEvent string `json:"attestation_event,omitempty"`
	// BearerSecret authenticates as the slice's CURRENT bearer identity, in
	// place of every field above — a bearer slice has no identity capable
	// of signing a proof event, so presenting its secret is the entire proof
	// (mirrors claim_funds' own bearer path).
	BearerSecret string `json:"bearer_secret,omitempty"`

	NewIdentity jitTransferNewIdentityParam `json:"new_identity"`
}

type jitTransferResponse struct {
	AmountMloki   uint64 `json:"amount_mloki"`
	IdentityType  string `json:"identity_type"`
	IdentityValue string `json:"identity_value,omitempty"`
	// BearerSecret is populated only when new_identity.identity_type ==
	// "bearer", and only in this one response — it is never retrievable
	// again (NIP-JW §Bearer Slices).
	BearerSecret string `json:"bearer_secret,omitempty"`
}

// newIdentityHash binds a transfer proof to a specific target identity —
// the same role bolt11_hash plays for a claim proof (see
// verifyClaimIdentityEvent's doc comment). identityValue is "" for a bearer
// target, since the caller doesn't choose (or know) a bearer target's value
// ahead of generating it.
func newIdentityHash(identityType, identityValue string) string {
	sum := sha256.Sum256([]byte(identityType + ":" + identityValue))
	return hex.EncodeToString(sum[:])
}

func (controller *nip47Controller) HandleJITTransferEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, tags nostr.Tags) {
	params := &jitTransferParams{}
	resp := decodeRequest(nip47Request, params)
	if resp != nil {
		publishResponse(resp, tags)
		return
	}

	isBearerCurrent := params.BearerSecret != ""

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Str("new_identity_type", params.NewIdentity.IdentityType).
		Bool("bearer_current", isBearerCurrent).
		Msg("Handling jit_transfer request")

	// 1. jit_transfer only ever makes sense against a jit_wallet.
	if app.Kind != db.AppKindJITWallet {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "jit_transfer requires a jit_wallet app")
		return
	}

	// 2. Rate limit — shares jitClaimLimiter (and its budget) with jit_redeem,
	// deliberately: transferring OUT of a bearer slice is also a
	// secret-presentation surface with no signature to forge, exactly like
	// redeeming one. If the two methods had separate budgets, an attacker
	// could double their effective guess allowance by splitting attempts
	// across both.
	if !controller.jitClaimLimiter.Allow(app.AppPubkey, controller.cfg.GetEnv().JITWalletClaimRateLimitPerHour) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RATE_LIMITED, "rate limit exceeded for jit_transfer")
		return
	}

	// 3. Determine the caller's current identity. A bearer-secret proof and
	// an identity-bound one are mutually exclusive param shapes.
	var currentIdentityType, currentIdentityValue string
	if isBearerCurrent {
		if params.IdentityType != "" || params.IdentityValue != "" || params.IdentityEvent != "" || params.AttestationEvent != "" {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				"bearer_secret is mutually exclusive with identity_type, identity_value, identity_event, and attestation_event")
			return
		}
		secretBytes, hexErr := hex.DecodeString(params.BearerSecret)
		if hexErr != nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "bearer_secret must be hex")
			return
		}
		hash := sha256.Sum256(secretBytes)
		currentIdentityType = db.JITAllocIdentityBearer
		currentIdentityValue = hex.EncodeToString(hash[:])
	} else {
		if params.IdentityType == "" || params.IdentityValue == "" || params.IdentityEvent == "" {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				"identity_type, identity_value, and identity_event are all required")
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
		currentIdentityType = params.IdentityType
		currentIdentityValue = params.IdentityValue
	}

	// 4. new_identity's own type must be recognized before anything else
	// checks it — computed here (from the raw request, not yet validated
	// for trust) so the proof-binding hash below reflects exactly what the
	// caller asked for, independent of whether it turns out to be valid.
	newIdentityType := params.NewIdentity.IdentityType
	if newIdentityType != db.JITAllocIdentityPubkey && newIdentityType != db.JITAllocIdentityConnectionKey && newIdentityType != db.JITAllocIdentityBearer {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			fmt.Sprintf("new_identity.identity_type must be %q, %q, or %q",
				db.JITAllocIdentityPubkey, db.JITAllocIdentityConnectionKey, db.JITAllocIdentityBearer))
		return
	}
	if newIdentityType == db.JITAllocIdentityBearer && (params.NewIdentity.IdentityValue != "" || params.NewIdentity.IAPubkey != "") {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			"new_identity must not carry identity_value or ia_pubkey when identity_type is bearer")
		return
	}
	targetHash := newIdentityHash(newIdentityType, params.NewIdentity.IdentityValue)

	// 5. Read-only lookup of the slice being transferred BEFORE touching the
	// atomic transfer guard, so a proof that fails verification never
	// briefly occupies (and can never grief) a legitimate concurrent
	// transfer or claim — same ordering rationale as jit_redeem.
	claim, err := controller.appsService.GetJITWalletClaim(app.ID, currentIdentityType, currentIdentityValue)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to look up JIT wallet claim")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to look up claim")
		return
	}
	if claim == nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_NOT_FOUND, "no unclaimed slice for this identity")
		return
	}

	// 6. Verify the caller is authorized to transfer this slice.
	if !isBearerCurrent {
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
		if currentIdentityType == db.JITAllocIdentityConnectionKey {
			if err := json.Unmarshal([]byte(params.AttestationEvent), &attestationEvent); err != nil {
				respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "attestation_event is not valid JSON")
				return
			}
			attestationEventID = attestationEvent.ID
		}
		if err := verifyTransferIdentityEvent(&identityEvent, currentIdentityType, currentIdentityValue, walletPubkey, targetHash, attestationEventID); err != nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, err.Error())
			return
		}

		// Same live IA re-check jit_redeem does — a compromised/retired
		// Identity Authority must be cut off immediately, including for
		// jit_transfer, not only jit_redeem.
		if currentIdentityType == db.JITAllocIdentityConnectionKey {
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
			if err := verifyClaimAttestationEvent(&attestationEvent, claim.IAPubkey, identityEvent.PubKey, currentIdentityValue); err != nil {
				respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, err.Error())
				return
			}
		}
	}
	// isBearerCurrent: nothing further to verify — step 5's hash-matched
	// lookup was the entire proof, identical in spirit to a bearer redeem.

	// 7. Validate new_identity now that the caller is authenticated:
	// pubkey/connection_key shape + live IA trust (reuses exactly the
	// validation create_jit_wallet applies to a recipient entry — not
	// duplicated), or the bearer-exclusivity rule for a bearer target.
	var newIdentityValueToStore, newIAPubkeyToStore, bearerSecretForResponse string
	switch newIdentityType {
	case db.JITAllocIdentityBearer:
		// A bearer slice never shares a wallet with another recipient
		// (NIP-JW §Bearer Slices) — transferring INTO bearer is only legal
		// when this is already the wallet's only unclaimed slice, so it
		// stays that way rather than picking up a bearer sibling next to
		// this now-anonymous one.
		allClaims, err := controller.appsService.ListClaimsForWallet(app.ID)
		if err != nil {
			logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to list JIT wallet claims")
			respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to list claims")
			return
		}
		unclaimedCount := 0
		for _, c := range allClaims {
			if c.ClaimedAt == nil {
				unclaimedCount++
			}
		}
		if unclaimedCount != 1 {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				"a bearer slice cannot share a wallet with other recipients; this wallet has more than one unclaimed slice")
			return
		}
		secretHex, secretHash, genErr := jitwallet.GenerateBearerSecret()
		if genErr != nil {
			logger.Logger.Error().Err(genErr).Uint("app_id", app.ID).Msg("Failed to generate bearer secret for jit_transfer")
			respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to generate bearer secret")
			return
		}
		newIdentityValueToStore = secretHash
		bearerSecretForResponse = secretHex
	default:
		if err := jitwallet.ValidateIdentityShape(jitwallet.Deps{IAChecker: controller.iaChecker},
			newIdentityType, params.NewIdentity.IdentityValue, params.NewIdentity.IAPubkey); err != nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, err.Error())
			return
		}
		newIdentityValueToStore = params.NewIdentity.IdentityValue
		newIAPubkeyToStore = params.NewIdentity.IAPubkey
	}

	// 8. Atomically transfer the slice — guards against races and enforces
	// the wallet's max_transfers cap.
	amount, err := controller.appsService.TransferJITWalletSlice(app.ID,
		currentIdentityType, currentIdentityValue,
		newIdentityType, newIdentityValueToStore, newIAPubkeyToStore)
	if err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, err.Error())
		return
	}

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Str("new_identity_type", newIdentityType).
		Msg("JIT wallet slice transferred")

	result := jitTransferResponse{
		AmountMloki:  uint64(amount), //nolint:gosec // AmountMloki is always non-negative
		IdentityType: newIdentityType,
	}
	if newIdentityType == db.JITAllocIdentityBearer {
		result.BearerSecret = bearerSecretForResponse
	} else {
		result.IdentityValue = newIdentityValueToStore
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result:     result,
	}, tags)
}

// verifyTransferIdentityEvent checks a kind-35521 transfer proof — the same
// event kind and most of the same checks as verifyClaimIdentityEvent
// (claim_funds_controller.go), except bound to the transfer's *target*
// identity via a new_identity_hash tag instead of an invoice's bolt11_hash.
// That binding closes the same class of attack the invoice binding closes
// for jit_redeem: an intercepted proof can't be resubmitted with a
// different new_identity than the one it was actually signed for.
//
// Deliberately a separate function rather than a refactor of
// verifyClaimIdentityEvent to accept either binding: that function is
// already security-critical and covered by its own focused tests: safer to
// duplicate its structure once, here, than to risk it while generalizing it.
func verifyTransferIdentityEvent(ev *nostr.Event, identityType, identityValue, walletPubkey, newIdentityHashTag, attestationEventID string) error {
	if ev.Kind != nostrKindClaimProof {
		return fmt.Errorf("identity_event must be kind %d, got %d", nostrKindClaimProof, ev.Kind)
	}
	valid, err := ev.CheckSignature()
	if err != nil || !valid {
		return fmt.Errorf("identity_event has invalid signature")
	}
	if !ev.CheckID() {
		return fmt.Errorf("identity_event id does not match its content")
	}
	dTag := ev.Tags.Find("d")
	if len(dTag) < 2 || dTag[1] != walletPubkey {
		return fmt.Errorf("identity_event d-tag does not match this wallet")
	}
	hashTag := ev.Tags.Find("new_identity_hash")
	if len(hashTag) < 2 || hashTag[1] != newIdentityHashTag {
		return fmt.Errorf("identity_event is not bound to this new_identity")
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
