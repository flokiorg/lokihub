package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
)

// cashTransferNewIdentityParam/cashTransferParams below are no longer used to
// parse the real request (HandleCashTransferEvent decodes straight into
// github.com/ohstr/nmilat/nipcash's own exported CashTransferRequest now -
// nmilat migration, PR #90) - they're kept purely as this package's own test
// fixtures. nipcash.CashTransferRequest's own NewIdentity field type is
// unexported inside nipcash (nmilat's own internal wire-shape detail), so a
// test can't build one as `nipcash.CashTransferRequest{NewIdentity:
// SomeType{...}}` in a single literal; every existing test here instead
// builds this local, structurally-identical type and marshals it to JSON
// (see handleCashTransferFor), which is exactly how a real NWC client's
// request arrives anyway - so nothing downstream needs to change.
type cashTransferNewIdentityParam struct {
	IdentityType  string `json:"identity_type"` // "pubkey" | "connection_key" | "bearer"
	IdentityValue string `json:"identity_value,omitempty"`
	IAPubkey      string `json:"ia_pubkey,omitempty"` // required iff identity_type == connection_key
}

type cashTransferParams struct {
	// The caller's CURRENT registered identity — mutually exclusive with
	// BearerSecret. IdentityEvent is the JSON-encoded kind-23198 transfer
	// proof, signed fresh for this call and bound to this wallet + this
	// specific new_identity (see verifyTransferIdentityEvent).
	IdentityType     string `json:"identity_type,omitempty"`
	IdentityValue    string `json:"identity_value,omitempty"`
	IdentityEvent    string `json:"identity_event,omitempty"`
	AttestationEvent string `json:"attestation_event,omitempty"`
	// BearerSecret authenticates as the slice's CURRENT bearer identity, in
	// place of every field above — a bearer slice has no identity capable
	// of signing a proof event, so presenting its secret is the entire proof
	// (mirrors cash_redeem's own bearer path).
	BearerSecret string `json:"bearer_secret,omitempty"`

	NewIdentity cashTransferNewIdentityParam `json:"new_identity"`

	// AmountMillis is OPTIONAL — omitted, or equal to the slice's current full
	// amount, means "transfer it all" (this method's only behavior before
	// splitting existed). A value LESS than the slice's current amount
	// splits off exactly that much into a brand-new dedicated cash_wallet,
	// leaving the remainder behind on THIS slice, under the SAME current
	// identity, otherwise untouched — the "$20 bill, hand over $5, keep $15
	// in change" model (NIP-CASH §Splitting a Slice). The carved-off amount,
	// and any nonzero remainder left behind, must each be at least this
	// slice's own MinTransferMloki (0 = no floor) — enforced by
	// AppsService.SplitCashSliceAmount.
	AmountMillis *uint64 `json:"amount_millis,omitempty"`

	// MintSignature opts a spun-off wallet's token into mint provenance
	// (NIP-CASH §Mint Provenance) — only meaningful when this call actually
	// spins off a dedicated wallet (a split, or a full transfer to bearer on
	// a multi-recipient-history wallet); harmless no-op on an in-place
	// reassignment, which never mints a new token to sign.
	MintSignature bool `json:"mint_signature,omitempty"`
}

// cashTransferResponse never carries a secret of any kind — IdentityValue is
// always either a public identity (pubkey/connection_key) or a one-way
// commitment the caller already supplied for a bearer target, deliberately
// never a value the wallet itself generated (see the bearer branch of
// HandleCashTransferEvent for why: this response travels over the shared
// cash_wallet connection, decryptable by every recipient who ever held it).
type cashTransferResponse struct {
	AmountMillis  uint64 `json:"amount_millis"`
	IdentityType  string `json:"identity_type"`
	IdentityValue string `json:"identity_value,omitempty"`
	// RemainingAmountMillis is populated only when this call went through the
	// split path (see NewWalletPubkey/NewWalletToken below): 0 for a full
	// split, >0 for a partial one — so the caller's own client can update
	// its cached view of what's left on THIS connection without a separate
	// list_recipients round-trip. Never populated for an in-place
	// reassignment (nothing was carved off; the whole slice just changed
	// hands, still for its original, unchanged amount).
	RemainingAmountMillis *uint64 `json:"remaining_amount_millis,omitempty"`
	// NewWalletPubkey and NewWalletToken are populated only when this
	// transfer split the slice's value — full or partial — off into a
	// brand-new dedicated cash_wallet instead of reassigning identity in
	// place. See HandleCashTransferEvent's routing comment for exactly when
	// each outcome applies.
	//
	// NewWalletPubkey is the new wallet's own WalletPubkey, in the clear —
	// safe to return unencrypted since a bare pubkey with no secret grants
	// nothing (same reasoning as create_circle_wallet_controller.go's own
	// plaintext WalletPubkey field). It exists so the recipient can derive
	// the correct decryption key for NewWalletToken: a fresh one-off keypair
	// generated only for this response would never reach them any other way.
	//
	// NewWalletToken is that new wallet's lokicash1... connection token,
	// NIP-44 encrypted to the pubkey that signed this call's identity_event
	// using the new wallet's own keypair (NewWalletPubkey / the matching
	// server-held privkey) — a second, inner encryption layer nested inside
	// this response's own normal per-connection encryption. Every
	// co-recipient of THIS wallet's shared connection can still decrypt the
	// outer response same as always, but only the caller who just proved
	// ownership of the slice being split can decrypt this field. See
	// NIP-CASH "Splitting a Slice Off Into a Dedicated Wallet".
	NewWalletPubkey string `json:"new_wallet_pubkey,omitempty"`
	NewWalletToken  string `json:"new_wallet_token,omitempty"`
	// RemainderWalletPubkey/RemainderWalletToken carry the caller's own change,
	// now in its OWN fresh dedicated wallet, for a PARTIAL split (NIP-CASH
	// §Splitting a Slice: a partial split consumes the source and mints two
	// wallets — carved for new_identity, remainder for the caller — so a
	// wallet's committed amount is never rewritten in place). Absent for a full
	// transfer (no remainder) and for an in-place reassignment. Delivered the
	// same nested-encrypted way as NewWalletToken.
	RemainderWalletPubkey string `json:"remainder_wallet_pubkey,omitempty"`
	RemainderWalletToken  string `json:"remainder_wallet_token,omitempty"`
}

// newIdentityHash binds a transfer proof to a specific target identity —
// the same role bolt11_hash plays for a claim proof (see
// verifyClaimIdentityEvent's doc comment). identityValue is "" for a bearer
// target, since the caller doesn't choose (or know) a bearer target's value
// ahead of generating it. iaPubkey is included so a connection_key target's
// Identity Authority is part of what the signer committed to — omitting it
// (2026-07-30 audit finding) let a captured proof be resubmitted with a
// DIFFERENT, still-trusted IA swapped in, redirecting who is authoritative to
// redeem the transferred slice even though identity_value (the connection_key
// string itself) never changed. Always "" for pubkey/bearer targets, which
// have no ia_pubkey concept, so their hash is unaffected by this field.
// noSliceRegisteredMessage is the NOT_FOUND message for a claim lookup that
// found no UNCLAIMED slice for (walletAppID, identityType, identityValue) —
// shared by cash_transfer and cash_redeem. Best-effort enriches the generic
// message with a breadcrumb when the slice DOES exist but was already split
// off into a new dedicated wallet: exactly the situation a client built
// against the pre-two-wallet-split contract hits on its natural retry for a
// "remainder" it believes it still holds on this connection (independent
// audit finding, full-review pass — data/docs/audits/cash-consolidate-2026-08-29/).
// This doesn't fix that client's underlying assumption, but turns a silent
// dead end into an actionable one. Diagnostic-only: any failure in the lookup
// itself silently falls back to the generic message rather than affecting
// the response.
func (controller *nip47Controller) noSliceRegisteredMessage(walletAppID uint, identityType, identityValue string) string {
	const generic = "no slice registered for this identity"
	var priorClaim db.CashWalletClaim
	err := controller.db.Where(
		"wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NOT NULL AND spun_off_to_wallet_app_id IS NOT NULL",
		walletAppID, identityType, identityValue,
	).First(&priorClaim).Error
	if err != nil {
		return generic
	}
	return generic + " — its value was already split off into a new dedicated wallet by an earlier call; " +
		"check list_recipients or that earlier call's own response for the new connection, this one no longer holds it"
}

func newIdentityHash(identityType, identityValue, iaPubkey string) string {
	sum := sha256.Sum256([]byte(identityType + ":" + identityValue + ":" + iaPubkey))
	return hex.EncodeToString(sum[:])
}

func (controller *nip47Controller) HandleCashTransferEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, tags nostr.Tags) {
	params := &nipcash.CashTransferRequest{}
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
		Msg("Handling cash_transfer request")

	// 1. cash_transfer only ever makes sense against a cash_wallet.
	if app.Kind != db.AppKindCashWallet {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "cash_transfer requires a cash_wallet app")
		return
	}

	// 2. Rate limit — shares cashClaimLimiter (and its budget) with cash_redeem,
	// deliberately: transferring OUT of a bearer slice is also a
	// secret-presentation surface with no signature to forge, exactly like
	// redeeming one. If the two methods had separate budgets, an attacker
	// could double their effective guess allowance by splitting attempts
	// across both.
	if !controller.cashClaimLimiter.Allow(app.AppPubkey, controller.cfg.GetEnv().CashWalletClaimRateLimitPerHour) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RATE_LIMITED, "rate limit exceeded for cash_transfer")
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
		currentIdentityType = db.CashIdentityBearer
		currentIdentityValue = hex.EncodeToString(hash[:])
	} else {
		if params.IdentityType == "" || params.IdentityValue == "" || params.IdentityEvent == "" {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				"identity_type, identity_value, and identity_event are all required")
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
		currentIdentityType = params.IdentityType
		currentIdentityValue = params.IdentityValue
	}

	// 4. new_identity's own type must be recognized before anything else
	// checks it — computed here (from the raw request, not yet validated
	// for trust) so the proof-binding hash below reflects exactly what the
	// caller asked for, independent of whether it turns out to be valid.
	newIdentityType := params.NewIdentity.IdentityType
	if newIdentityType != db.CashIdentityPubkey && newIdentityType != db.CashIdentityConnectionKey && newIdentityType != db.CashIdentityBearer {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			fmt.Sprintf("new_identity.identity_type must be %q, %q, or %q",
				db.CashIdentityPubkey, db.CashIdentityConnectionKey, db.CashIdentityBearer))
		return
	}
	// Unlike mint_cash's bearer recipient (identity_value forbidden —
	// the Hub generates it), a bearer new_identity here MUST carry a
	// caller-supplied identity_value: see the bearer branch below for why.
	// Shape is validated there, after the proof; this hash binds the proof to
	// whatever the caller submitted, valid or not (including IAPubkey, "" if
	// omitted), exactly as it does for every other target type/field.
	targetHash := newIdentityHash(newIdentityType, params.NewIdentity.IdentityValue, params.NewIdentity.IAPubkey)

	// 5. Read-only lookup of the slice being transferred BEFORE touching the
	// atomic transfer guard, so a proof that fails verification never
	// briefly occupies (and can never grief) a legitimate concurrent
	// transfer or claim — same ordering rationale as cash_redeem.
	claim, err := controller.appsService.GetCashWalletClaim(app.ID, currentIdentityType, currentIdentityValue)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to look up Cash wallet claim")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to look up claim")
		return
	}
	if claim == nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_NOT_FOUND,
			controller.noSliceRegisteredMessage(app.ID, currentIdentityType, currentIdentityValue))
		return
	}

	// 6. Resolve the requested transfer amount. Omitted, or equal to the
	// slice's current full amount as read just above, means "transfer it
	// all". Anything less is a partial split. This read is a best-effort
	// bound only — the atomic operation below (ReassignCashSliceIdentity or
	// SplitCashSliceAmount) always re-validates against the slice's live
	// state with its own fresh read, so a stale value here (a concurrent
	// split shrinking the slice between this read and that one) can only
	// ever produce a safe rejection under a genuine race, never an incorrect
	// transfer.
	requestedAmount := uint64(claim.AmountMloki) //nolint:gosec // AmountMloki is always non-negative
	isFullTransfer := true
	if params.AmountMillis != nil {
		if *params.AmountMillis == 0 {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "amount_millis must be positive")
			return
		}
		if *params.AmountMillis > requestedAmount {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				fmt.Sprintf("amount_millis %d exceeds this slice's own balance of %d", *params.AmountMillis, requestedAmount))
			return
		}
		requestedAmount = *params.AmountMillis
		isFullTransfer = requestedAmount == uint64(claim.AmountMloki) //nolint:gosec
	}

	// 7. Verify the caller is authorized to transfer this slice.
	//
	// callerProofPubkey is the pubkey that signed identity_event — the
	// caller's own authenticated identity, captured here (rather than
	// re-derived later) since it's only available while identityEvent is in
	// scope. Used only by the split path below, to encrypt that new wallet's
	// connection to the caller and no one else. Stays empty when
	// isBearerCurrent, which is fine: a bearer slice's wallet can never be
	// multi-recipient (see the invariant note in the bearer branch below),
	// so a bearer-current split (which can only happen via a PARTIAL amount,
	// since a full bearer-current transfer is always in-place) still only
	// ever needs the bearer secret itself as proof — there is no signed
	// identity_event to pull a caller pubkey from either way, so the
	// recipient-facing encryption for that case is skipped below.
	var callerProofPubkey string
	// proofEventID is identityEvent.ID, captured for the same "only available
	// while identityEvent is in scope" reason as callerProofPubkey above.
	// Used only by the split path's rollback (handleCashTransferSplit) to
	// release the single-use replay guard below if the split fails and rolls
	// back, so a caller who hits a transient failure can retry with the
	// exact same proof rather than needing a brand-new one. Stays empty for
	// isBearerCurrent, same as callerProofPubkey.
	var proofEventID string
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
		if currentIdentityType == db.CashIdentityConnectionKey {
			if err := json.Unmarshal([]byte(params.AttestationEvent), &attestationEvent); err != nil {
				respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "attestation_event is not valid JSON")
				return
			}
			attestationEventID = attestationEvent.ID
		}
		if err := verifyTransferIdentityEvent(&identityEvent, currentIdentityType, currentIdentityValue, walletPubkey, targetHash, requestedAmount, attestationEventID); err != nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, err.Error())
			return
		}
		// NOTE: the single-use replay guard (db.CashTransferProof) is
		// deliberately NOT inserted here — only once every remaining check
		// that can reject this request for a reason unrelated to the atomic
		// operation itself (the live IA-trust re-check below, and step 8's
		// new_identity validation) has also passed. A proof that fails one of
		// those later checks never touched the slice, so burning it here
		// would force a legitimate caller to sign an entirely new kind-23198
		// event to retry even though nothing happened (2026-07-30 audit
		// finding). callerProofPubkey/proofEventID are captured now, while
		// identityEvent is in scope, but the actual insert happens right
		// before step 9's split/reassign decision.
		callerProofPubkey = identityEvent.PubKey
		proofEventID = identityEvent.ID

		// Same live IA re-check cash_redeem does — a compromised/retired
		// Identity Authority must be cut off immediately, including for
		// cash_transfer, not only cash_redeem.
		if currentIdentityType == db.CashIdentityConnectionKey {
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

	// 8. Validate new_identity now that the caller is authenticated:
	// pubkey/connection_key shape + live IA trust (reuses exactly the
	// validation mint_cash applies to a recipient entry — not
	// duplicated), or shape validation for a bearer target's caller-supplied
	// commitment.
	var newIdentityValueToStore, newIAPubkeyToStore string
	switch newIdentityType {
	case db.CashIdentityBearer:
		// The bearer secret itself MUST be caller-supplied (as a commitment,
		// never the raw secret) rather than generated here and returned in
		// this response. Unlike mint_cash's bearer response — which
		// travels over the Hub's own single-owner connection — this response
		// travels over the SHARED cash_wallet connection, decryptable by every
		// recipient who ever held it (NIP-47 response encryption derives its
		// key from (clientPubkey, walletPrivkey), and every recipient shares
		// the same client keypair). A server-minted secret handed back here
		// would be readable by any co-recipient before the caller could ever
		// deliver it. The caller generates their own secret locally and keeps
		// it; the wallet only ever sees and stores its hash — mirroring how a
		// connection_key target is already caller-chosen, not server-issued.
		if params.NewIdentity.IAPubkey != "" {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				"new_identity must not carry ia_pubkey when identity_type is bearer")
			return
		}
		if decoded, decErr := hex.DecodeString(params.NewIdentity.IdentityValue); decErr != nil || len(decoded) != 32 {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				"new_identity.identity_value is required for a bearer target and must be a 64-character lowercase "+
					"hex commitment (sha256 of a secret you generate and keep yourself) — the wallet never mints "+
					"or returns a bearer secret over the shared connection")
			return
		}
		newIdentityValueToStore = params.NewIdentity.IdentityValue
	default:
		if err := cashwallet.ValidateIdentityShape(cashwallet.Deps{IAChecker: controller.iaChecker},
			newIdentityType, params.NewIdentity.IdentityValue, params.NewIdentity.IAPubkey); err != nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, err.Error())
			return
		}
		newIdentityValueToStore = params.NewIdentity.IdentityValue
		newIAPubkeyToStore = params.NewIdentity.IAPubkey
	}

	// Single-use replay guard: a captured proof (any co-recipient sharing
	// this cash_wallet connection can decrypt every request sent over it,
	// including this one) must not be resubmittable within its own freshness
	// window — see db.CashTransferProof's doc comment. Inserted here, after
	// every step-7/8 check that doesn't touch the slice has already passed,
	// so a request that fails one of THOSE checks never burns the proof (see
	// the comment above callerProofPubkey/proofEventID's capture in step 7).
	// Empty for isBearerCurrent, which has no signed proof to consume.
	if proofEventID != "" {
		if err := controller.db.Create(&db.CashTransferProof{AppID: app.ID, EventID: proofEventID}).Error; err != nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "identity_event has already been used")
			return
		}
	}

	// 9. Decide in-place reassignment vs. split.
	//
	// A PARTIAL transfer (requestedAmount < the slice's current full amount)
	// ALWAYS splits off a brand-new dedicated wallet for the carved-off
	// piece: there is no "in-place" concept for a partial amount, since the
	// giver keeps the remainder under their own, unchanged identity — only
	// the carved-off piece needs a home, and it always gets a fresh
	// connection, never one shared with the remainder or anyone else.
	//
	// A FULL transfer (requestedAmount == the slice's current full amount)
	// to a pubkey/connection_key target is ALWAYS reassigned in place —
	// exactly today's behavior, unconditional on the wallet's recipient
	// history, since redeeming or transferring an identity-bound slice
	// always requires a real signed proof, never just presenting a shared
	// secret, so reusing the connection is safe regardless of who else has
	// ever held it.
	//
	// A FULL transfer to a BEARER target reassigns in place ONLY if the
	// wallet has (and has always had) exactly one recipient — exactly
	// today's bearer-mixing rule, unchanged: a bearer redemption/transfer's
	// entire proof is its raw secret, transmitted in the request body, so
	// handing a bearer note a connection any former co-recipient might
	// still be listening on would hand them everything needed to steal it
	// the moment it's ever redeemed. Every other bearer-target case (a
	// wallet that has ever had more than one recipient) splits instead —
	// see handleCashTransferSplit.
	split := !isFullTransfer
	if isFullTransfer && newIdentityType == db.CashIdentityBearer {
		allClaims, err := controller.appsService.ListClaimsForWallet(app.ID)
		if err != nil {
			logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to list Cash wallet claims")
			respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to list claims")
			return
		}
		split = len(allClaims) != 1
		if split && isBearerCurrent {
			// Structurally unreachable: a bearer slice's wallet can never
			// have more than one recipient (cashwallet.Resolve requires a
			// bearer recipient to be the wallet's only one at creation, and
			// no later operation ever adds a second claim row to an existing
			// wallet), so isBearerCurrent implies allClaims is exactly 1.
			// Rejected defensively rather than trusting that invariant blindly.
			respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED,
				"a bearer slice's wallet can never have more than one recipient")
			return
		}
	}

	if !split {
		// In-place reassignment only ever applies to a full transfer —
		// AmountMillis is unchanged by construction, so there's nothing to
		// pass beyond the identities themselves.
		amount, err := controller.appsService.ReassignCashSliceIdentity(app.ID,
			currentIdentityType, currentIdentityValue,
			newIdentityType, newIdentityValueToStore, newIAPubkeyToStore)
		if err != nil {
			// A race loss against a concurrent claim/transfer of this exact
			// slice (constants.ErrNotFound) gets NOT_FOUND, matching
			// cash_redeem's identical race-loss case — not the generic
			// BAD_REQUEST every other validation failure here uses, since a
			// race loss isn't caused by anything wrong with the request itself.
			// ReassignCashSliceIdentity's ErrNotFound-wrapped errors are
			// deliberately crafted, safe-to-show messages ("no slice
			// registered...", "already been redeemed or transferred"); any
			// OTHER error is a raw, unwrapped internal failure (e.g. a DB
			// error) that must never be echoed to the caller verbatim —
			// mirrors cash_redeem's and cash_consolidate's fixed-message
			// pattern for the same class of failure.
			code := constants.ERROR_BAD_REQUEST
			message := "failed to reassign this slice's identity"
			if errors.Is(err, constants.ErrNotFound) {
				code = constants.ERROR_NOT_FOUND
				message = err.Error()
			} else {
				logger.Logger.Error().Err(err).Uint("app_id", app.ID).
					Msg("Unexpected failure reassigning a cash_transfer slice's identity")
			}
			// Release the single-use replay guard: ReassignCashSliceIdentity
			// is one atomic UPDATE with no fund movement of its own — unlike
			// the split path, a failure here means nothing happened at all,
			// so (mirroring handleCashTransferSplit's own rollback) this
			// proof never actually authorized a completed operation and must
			// stay usable for a retry within its own freshness window.
			// Best-effort: if this delete itself fails, the proof stays
			// consumed and a legitimate retry needs a fresh one — safe (just
			// an availability inconvenience), never a security gap.
			if proofEventID != "" {
				if delErr := controller.db.Where("event_id = ?", proofEventID).Delete(&db.CashTransferProof{}).Error; delErr != nil {
					logger.Logger.Error().Err(delErr).Uint("app_id", app.ID).
						Msg("Failed to release cash_transfer proof replay guard after a lost in-place reassignment race")
				}
			}
			respondError(publishResponse, nip47Request.Method, code, message)
			return
		}

		logger.Logger.Info().
			Uint("app_id", app.ID).
			Str("new_identity_type", newIdentityType).
			Msg("Cash wallet slice transferred")

		// IdentityValue is safe to echo back for every mode, including
		// bearer: it is always either a public identity (pubkey/
		// connection_key) or a one-way commitment the caller already
		// supplied — never a secret the wallet itself generated.
		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Result: cashTransferResponse{
				AmountMillis:  uint64(amount), //nolint:gosec // AmountMloki is always non-negative
				IdentityType:  newIdentityType,
				IdentityValue: newIdentityValueToStore,
			},
		}, tags)
		return
	}

	controller.handleCashTransferSplit(ctx, nip47Request, app, currentIdentityType, currentIdentityValue, requestedAmount,
		newIdentityType, newIdentityValueToStore, newIAPubkeyToStore, callerProofPubkey, proofEventID, params.MintSignature, claim, publishResponse, tags)
}

// handleCashTransferSplit moves a full or partial amount out of a slice and
// into a brand-new, dedicated, single-slice cash_wallet under the same hub —
// the unified split path HandleCashTransferEvent's step 9 routes to whenever
// an in-place reassignment isn't safe or doesn't apply (see that step's own
// comment for exactly when). newIdentityType/newIdentityValueToStore/
// newIAPubkeyToStore describe the new wallet's sole recipient (any identity
// mode, not bearer-only); recipientPubkey is the pubkey that just proved
// ownership of the slice being split (identity_event's signer, empty for a
// bearer-current caller) — the new wallet's connection is encrypted to this
// pubkey and no one else, so it never touches the shared connection this
// response itself travels over.
//
// Ordering mirrors cash_redeem_controller.go's own claim-then-pay-then-
// rollback-on-failure shape: the source slice is claimed or decremented
// FIRST (an exclusive, atomic commit point — see SplitCashSliceAmount), then
// the new wallet is created and funded; any failure after that rolls it back
// (UnclaimCashSlice for a full split, UndoCashSliceSplit for a partial one),
// so a caller who hits an error here can safely retry — including with the
// exact same identity_event, since rollback also releases proofEventID's
// single-use guard (db.CashTransferProof) rather than leaving a proof that
// never actually authorized anything permanently burned.
func (controller *nip47Controller) handleCashTransferSplit(ctx context.Context, nip47Request *models.Request, app *db.App,
	currentIdentityType, currentIdentityValue string, requestedAmount uint64,
	newIdentityType, newIdentityValueToStore, newIAPubkeyToStore, recipientPubkey, proofEventID string, mintSignature bool,
	claim *db.CashWalletClaim, publishResponse publishFunc, tags nostr.Tags) {
	// Enforce the slice's own min_transfer_millis floor (0 = none) on BOTH the
	// carved piece and the remainder it would leave behind — a split that would
	// carve off, or leave behind, unmovable dust is rejected before anything is
	// claimed (NIP-CASH §Splitting a Slice). The old decrement-based path
	// enforced this inside SplitCashSliceAmount; the terminal-claim path does it
	// here. claim.AmountMloki is immutable (no operation rewrites it in place),
	// so this pre-claim read can't be raced smaller underneath the claim below.
	if floor := uint64(claim.MinTransferMloki); floor > 0 { //nolint:gosec // MinTransferMloki is non-negative
		remainder := uint64(claim.AmountMloki) - requestedAmount //nolint:gosec // requestedAmount <= claim.AmountMloki, bounded by step 6
		if requestedAmount < floor {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				fmt.Sprintf("amount_millis %d is below this slice's min_transfer_millis floor of %d", requestedAmount, floor))
			return
		}
		if remainder != 0 && remainder < floor {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				fmt.Sprintf("the %d remainder this split would leave behind is below this slice's min_transfer_millis floor of %d", remainder, floor))
			return
		}
	}

	// Claim the source slice TERMINAL — the operation's commit point. Its whole
	// amount re-emerges as one or two fresh dedicated wallets; nothing is ever
	// decremented in place, so a wallet's committed amount stays immutable for
	// its life (NIP-CASH §Splitting a Slice). This replaces the old
	// decrement-in-place partial split.
	claimedFull, err := controller.appsService.ClaimCashSlice(app.ID, currentIdentityType, currentIdentityValue)
	if err != nil {
		// Same NOT_FOUND-vs-BAD_REQUEST distinction as the in-place transfer
		// path above — a race loss is NOT_FOUND, everything else BAD_REQUEST.
		// Same err.Error()-is-only-safe-for-the-ErrNotFound-case reasoning too:
		// any other failure is a raw internal error that must not be echoed
		// to the caller verbatim.
		code := constants.ERROR_BAD_REQUEST
		message := "failed to claim this slice"
		if errors.Is(err, constants.ErrNotFound) {
			code = constants.ERROR_NOT_FOUND
			message = err.Error()
		} else {
			logger.Logger.Error().Err(err).Uint("app_id", app.ID).
				Msg("Unexpected failure claiming a cash_transfer slice for split")
		}
		respondError(publishResponse, nip47Request.Method, code, message)
		return
	}
	remainderAmount := uint64(claimedFull) - requestedAmount //nolint:gosec // claimedFull >= requestedAmount, bounded by HandleCashTransferEvent step 6

	// releaseProof drops the single-use proof replay guard so a caller who hits
	// a failure can retry with the identical proof, regardless of whether the
	// source claim itself gets restored.
	releaseProof := func() {
		if proofEventID != "" {
			if delErr := controller.db.Where("event_id = ?", proofEventID).Delete(&db.CashTransferProof{}).Error; delErr != nil {
				logger.Logger.Error().Err(delErr).Uint("app_id", app.ID).
					Msg("Failed to release cash_transfer proof replay guard after a rolled-back split")
			}
		}
	}
	// rollback releases the proof AND restores the source slice to its full
	// original amount. Only safe to call when the source's real balance
	// genuinely still backs that full amount — see the SplitInTwo call site
	// below for the case where it does not.
	rollback := func() {
		releaseProof()
		if unclaimErr := controller.appsService.UnclaimCashSlice(app.ID, currentIdentityType, currentIdentityValue); unclaimErr != nil {
			logger.Logger.Error().Err(unclaimErr).Uint("app_id", app.ID).
				Msg("Failed to roll back a claimed slice after a failed cash_transfer split")
		}
	}

	if app.ParentAppID == nil {
		rollback()
		logger.Logger.Error().Uint("app_id", app.ID).Msg("cash_wallet has no parent hub; cannot split off a slice")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to split off slice")
		return
	}
	hubApp := controller.appsService.GetAppById(*app.ParentAppID)
	if hubApp == nil {
		rollback()
		logger.Logger.Error().Uint("app_id", app.ID).Uint("hub_app_id", *app.ParentAppID).Msg("cash_wallet's parent hub no longer exists")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to split off slice")
		return
	}

	// Both new wallets inherit the source slice's own expiry, floor, and fee —
	// a split relocates an existing entitlement, never grants, shortens, or
	// lengthens one (NIP-CASH's inheritance rule). The carved piece goes to
	// new_identity; the remainder (when > 0) goes back to the caller's OWN
	// current identity in its own fresh wallet.
	result, sourceFundsIntact, err := cashwallet.SplitInTwo(ctx, cashwallet.Deps{
		AppsService:         controller.appsService,
		TransactionsService: controller.transactionsService,
		LNClient:            controller.lnClient,
		Keys:                controller.keys,
		DB:                  controller.db,
		RelayURLs:           controller.cfg.GetRelayUrls(),
	}, cashwallet.SplitInTwoParams{
		HubApp:                 hubApp,
		SourceWalletApp:        app,
		CarvedIdentityType:     newIdentityType,
		CarvedIdentityValue:    newIdentityValueToStore,
		CarvedIAPubkey:         newIAPubkeyToStore,
		CarvedAmountMloki:      requestedAmount,
		RemainderIdentityType:  currentIdentityType,
		RemainderIdentityValue: currentIdentityValue,
		RemainderIAPubkey:      claim.IAPubkey,
		RemainderAmountMloki:   remainderAmount,
		MinTransferMloki:       claim.MinTransferMloki,
		RedeemFeePpm:           claim.RedeemFeePpm,
		ExpiresAt:              app.ExpiresAt,
		SignMint:               mintSignature,
	})
	if err != nil {
		if sourceFundsIntact {
			rollback()
		} else {
			// The carved wallet's compensating reverse-transfer itself failed:
			// the source's real balance is short by the carved amount, which is
			// still sitting in the (deliberately retained) carved wallet. The
			// proof is released so the caller isn't permanently burned, but the
			// source claim is intentionally NOT restored — doing so would let
			// the caller believe they have the full original amount when the
			// wallet can't actually pay it out (independent security audit,
			// Auditor B, finding 2 — data/docs/audits/cash-consolidate-2026-08-29/).
			releaseProof()
			logger.Logger.Error().Uint("app_id", app.ID).
				Msg("Split-off compensation failed; source slice intentionally left claimed pending manual reconciliation")
		}
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to split off Cash wallet slice")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to split off slice")
		return
	}

	// deliver returns a new wallet's clear pubkey plus its token — nested-
	// encrypted to the caller's proof pubkey (so no co-recipient of THIS shared
	// connection can read it, exactly as before), or in the clear for a
	// bearer-current caller whose source wallet is structurally single-recipient
	// (there is no co-holder to defend against). Funds have already moved by
	// this point, so a delivery failure is operator-recoverable, never a
	// rollback (§Spinning a Slice Off).
	deliver := func(w *cashwallet.SplitResult) (pubkey, token string, ok bool) {
		if w.WalletApp.WalletPubkey != nil {
			pubkey = *w.WalletApp.WalletPubkey
		}
		if recipientPubkey == "" {
			return pubkey, w.CashToken, true
		}
		privKey, keyErr := controller.keys.GetAppWalletKey(w.WalletApp.ID)
		if keyErr != nil {
			logger.Logger.Error().Err(keyErr).Uint("new_wallet_id", w.WalletApp.ID).
				Msg("Failed to derive split-off wallet key; funds moved but could not be delivered")
			return "", "", false
		}
		enc, encErr := encryptPairingURI(recipientPubkey, privKey, w.CashToken)
		if encErr != nil {
			logger.Logger.Error().Err(encErr).Uint("new_wallet_id", w.WalletApp.ID).
				Msg("Failed to encrypt split-off wallet token; funds moved but could not be delivered")
			return "", "", false
		}
		return pubkey, enc, true
	}

	// Informational bookkeeping links (db.CashWalletClaim.SpunOffToWalletAppID /
	// db.App.SplitFromWalletAppID) — the source slice records the carved wallet
	// it spun off to, and the carved wallet records its source. Never fail here:
	// the funds have already moved and both wallets are live.
	if setErr := controller.appsService.SetCashSliceSplitTarget(app.ID, currentIdentityType, currentIdentityValue, result.Carved.WalletApp.ID); setErr != nil {
		logger.Logger.Error().Err(setErr).Uint("app_id", app.ID).Uint("new_wallet_id", result.Carved.WalletApp.ID).
			Msg("Failed to record split target on the source slice")
	}
	if setSrcErr := controller.appsService.SetCashWalletSplitSource(result.Carved.WalletApp.ID, app.ID); setSrcErr != nil {
		logger.Logger.Error().Err(setSrcErr).Uint("app_id", app.ID).Uint("new_wallet_id", result.Carved.WalletApp.ID).
			Msg("Failed to record split source on the carved wallet")
	}

	carvedPubkey, carvedToken, ok := deliver(result.Carved)
	if !ok {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL,
			"slice was split off but its connection could not be delivered; contact the wallet operator")
		return
	}

	resp := cashTransferResponse{
		AmountMillis:    requestedAmount,
		IdentityType:    newIdentityType,
		IdentityValue:   newIdentityValueToStore,
		NewWalletPubkey: carvedPubkey,
		NewWalletToken:  carvedToken,
	}
	if result.Remainder != nil {
		remPubkey, remToken, ok := deliver(result.Remainder)
		if !ok {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL,
				"slice was split off but the remainder connection could not be delivered; contact the wallet operator")
			return
		}
		if setSrcErr := controller.appsService.SetCashWalletSplitSource(result.Remainder.WalletApp.ID, app.ID); setSrcErr != nil {
			logger.Logger.Error().Err(setSrcErr).Uint("app_id", app.ID).Uint("new_wallet_id", result.Remainder.WalletApp.ID).
				Msg("Failed to record split source on the remainder wallet")
		}
		resp.RemainingAmountMillis = &remainderAmount
		resp.RemainderWalletPubkey = remPubkey
		resp.RemainderWalletToken = remToken
	}

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Uint("carved_wallet_id", result.Carved.WalletApp.ID).
		Uint64("carved_amount_mloki", requestedAmount).
		Uint64("remainder_amount_mloki", remainderAmount).
		Msg("Cash wallet slice split off into fresh dedicated wallet(s)")

	publishResponse(&models.Response{ResultType: nip47Request.Method, Result: resp}, tags)

	// The source slice is now terminal; auto-delete the source wallet if this
	// was its last unclaimed slice and its real balance is zero. Best-effort,
	// after the response — the expiry sweep remains the fallback.
	controller.maybeAutoDeleteDrainedCashWallet(app)
}

// maybeAutoDeleteDrainedCashWallet deletes app if it has no unclaimed slices
// left AND its real ledger balance is exactly zero — a multi-recipient
// wallet with other still-unclaimed slices must never be deleted just
// because one recipient's slice fully split away, since the wallet's real
// balance still legitimately backs those other slices. Best-effort: logged,
// never fails the caller's request, and the periodic expiry-sweep ticker
// (service.runCashCleanup) remains the fallback if this check's own balance
// read is ever unexpectedly nonzero (structurally shouldn't be, given the
// invariant that a cash_wallet's balance always equals the sum of its
// unclaimed slices' AmountMloki, but defensive rather than trusting that
// blindly and force-deleting a wallet that still holds real funds).
func (controller *nip47Controller) maybeAutoDeleteDrainedCashWallet(app *db.App) {
	claims, err := controller.appsService.ListClaimsForWallet(app.ID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to list claims for Cash wallet auto-delete check")
		return
	}
	for _, c := range claims {
		if c.ClaimedAt == nil {
			return // another slice is still unclaimed — keep the wallet alive
		}
	}
	balance := queries.GetIsolatedBalance(controller.db, app.ID)
	if balance != 0 {
		logger.Logger.Error().Uint("app_id", app.ID).Int64("balance", balance).
			Msg("Cash wallet has no unclaimed slices left but a nonzero balance; leaving it for the expiry sweep rather than force-deleting")
		return
	}
	if err := controller.appsService.DeleteApp(app); err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to auto-delete fully-drained Cash wallet")
		return
	}
	logger.Logger.Info().Uint("app_id", app.ID).Msg("Auto-deleted Cash wallet after its last slice was fully split away")
}

// verifyTransferIdentityEvent checks a kind-23198 transfer proof — the same
// event kind and most of the same checks as verifyClaimIdentityEvent
// (cash_redeem_controller.go), except bound to the transfer's *target*
// identity via a new_identity_hash tag instead of an invoice's bolt11_hash,
// AND to the specific amount_millis this request resolves to (NIP-CASH
// §Transferring and Splitting a Slice: "a proof MUST NOT be replayable to
// authorize a DIFFERENT amount_millis than the one it was signed for" — an
// omitted request amount_millis, i.e. a full transfer, is bound to the exact
// live amount resolved for it, never left unbound). Caller passes the
// already-resolved requestedAmount (§Processing Algorithm step 1's
// "treating an omitted amount_millis as bound to the slice's full current
// amount" rule already applied) — this function only compares it against
// what the proof itself commits to.
//
// The identity/target binding alone isn't enough once splitting exists: an
// in-place full reassignment naturally self-invalidates a replayed proof
// (the slice's registered identity changes, so the proof's signer no longer
// matches), but a partial split leaves the SAME identity registered on the
// source slice — so, unlike before splitting existed, a captured proof
// would otherwise remain "valid" for repeat reuse within its freshness
// window. Callers MUST additionally insert a db.CashTransferProof row (see
// its own doc comment) immediately after this function returns nil, to
// make every proof single-use regardless of amount.
//
// Deliberately a separate function rather than a refactor of
// verifyClaimIdentityEvent to accept either binding: that function is
// already security-critical and covered by its own focused tests: safer to
// duplicate its structure once, here, than to risk it while generalizing it.
func verifyTransferIdentityEvent(ev *nostr.Event, identityType, identityValue, walletPubkey, newIdentityHashTag string, requestedAmount uint64, attestationEventID string) error {
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
	amountTag := ev.Tags.Find("amount_millis")
	if len(amountTag) < 2 || amountTag[1] != strconv.FormatUint(requestedAmount, 10) {
		return fmt.Errorf("identity_event is not bound to this amount_millis")
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
