package controllers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
)

// consolidateSourceParam is one input slice to a consolidation: a signed
// kind-23198 identity_event proof against the source slice's current
// registered pubkey or connection_key identity (connection_key additionally
// carrying an attestation_event), plus the wallet_pubkey identifying which
// cash_wallet the slice lives in. Cash-mode sources remain deferred (see the
// handler) — its secret would sit in plaintext in a request encrypted only
// under the CALLING connection's shared key, decryptable by any co-recipient
// of a shared calling wallet with no claim on that foreign cash note.
// maxConsolidateSources mirrors mint_cash's/the hub config's own
// maxRecipientsPerWallet=100 cap (cashwallet/create.go, apps/cash_hub_service.go)
// — the same "cap every multi-item Cash batch" convention, applied here since
// this feature shipped without it. Without a ceiling, one rate-limited
// cash_consolidate request could bundle an arbitrary number of independent
// custody lookups and proof verifications, diluting the per-request
// cashClaimLimiter tick by however many sources are packed in.
const maxConsolidateSources = 100

// consolidateSourceParam is no longer used to parse the real request
// (HandleCashConsolidateEvent decodes straight into github.com/ohstr/nmilat/
// nipcash's own exported CashConsolidateRequest now - nmilat migration, PR
// #90) - kept purely as this package's own test fixture, for the same reason
// cash_transfer_controller.go keeps cashTransferNewIdentityParam: nipcash's
// own Sources element type is unexported, so a test can't build one in a
// single struct-literal expression the way this local type allows.
type consolidateSourceParam struct {
	WalletPubkey     string `json:"wallet_pubkey"`
	IdentityType     string `json:"identity_type,omitempty"`
	IdentityValue    string `json:"identity_value,omitempty"`
	IdentityEvent    string `json:"identity_event,omitempty"`
	AttestationEvent string `json:"attestation_event,omitempty"`
	CashSecret       string `json:"cash_secret,omitempty"`
}

type cashConsolidateParams struct {
	Sources     []consolidateSourceParam     `json:"sources"`
	NewIdentity cashTransferNewIdentityParam `json:"new_identity"`
}

type cashConsolidateResponse struct {
	AmountMillis uint64 `json:"amount_millis"`
	// NewWalletPubkey is the merged wallet's WalletPubkey in the clear; the
	// caller derives the decryption key for NewWalletToken from it plus their
	// own privkey (same nested-encryption delivery as a split, §Spinning a Slice
	// Off). NewWalletToken is the merged lokicash1... token, NIP-44 encrypted to
	// the caller (not new_identity) when new_identity is a pubkey, or sent in
	// the clear when it's cash/connection_key.
	NewWalletPubkey string `json:"new_wallet_pubkey"`
	NewWalletToken  string `json:"new_wallet_token"`
	ExpiresAt       *int64 `json:"expires_at,omitempty"`
}

// resolvedConsolidateSource is one source after custody + authorization checks
// have passed and before the atomic claim: the located wallet app, the slice's
// identity, and the slice's own inherited terms.
type resolvedConsolidateSource struct {
	walletApp     *db.App
	identityType  string
	identityValue string
	claim         *db.CashWalletClaim
	// proofPubkey is the real Nostr pubkey that signed this source's
	// identity_event — same as cash_transfer_controller.go's
	// callerProofPubkey. Used to encrypt the merged wallet's delivery to
	// the caller (see the delivery step below), not identityValue, since
	// a connection_key source's identityValue is a connection_key hash,
	// not a pubkey usable for ECDH.
	proofPubkey string
}

// HandleCashConsolidateEvent combines several same-hub slices this node
// custodies into one new cash token (NIP-CASH §Consolidating Tokens).
//
// SECURITY / STATUS: the validation, custody, same-hub, proof, sum/ceiling and
// earliest-expiry checks below are the novel surface; the fund movement and
// nested-encrypted delivery reuse cashwallet.Consolidate and encryptForRecipient,
// which are covered elsewhere. Covered end to end against a live node by
// integration/cash_consolidate_test.go and cash_consolidate_adversarial_test.go,
// on top of the unit/guard suites in this package and cashwallet.
//
// Scope: sources MAY be pubkey or connection_key identified (each proven by
// its own signed identity_event, connection_key additionally requiring a
// live-trusted attestation_event); new_identity MAY be pubkey, connection_key,
// or cash (the merged wallet is owned by that identity; its token is
// delivered encrypted to the CALLER when new_identity is a pubkey — same as
// cash_transfer — or in the clear within the outer NIP-47 response for
// connection_key/cash, which have no real pubkey to ECDH against). Cash-mode
// SOURCES remain deferred: unlike connection_key (a
// signed proof, immune to this), a cash-mode source's secret has no signature
// and would sit in plaintext in a request encrypted only under the CALLING
// connection's shared key, decryptable by any co-recipient of a shared
// calling wallet with no claim on that foreign cash note.
func (controller *nip47Controller) HandleCashConsolidateEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, tags nostr.Tags) {
	params := &nipcash.CashConsolidateRequest{}
	if resp := decodeRequest(nip47Request, params); resp != nil {
		publishResponse(resp, tags)
		return
	}

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Int("source_count", len(params.Sources)).
		Msg("Handling cash_consolidate request")

	if app.Kind != db.AppKindCashWallet {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "cash_consolidate requires a cash_wallet app")
		return
	}
	if len(params.Sources) < 2 {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "consolidate requires at least two sources")
		return
	}
	if len(params.Sources) > maxConsolidateSources {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			fmt.Sprintf("at most %d sources per consolidate, got %d", maxConsolidateSources, len(params.Sources)))
		return
	}
	// The merged wallet's owner: pubkey or connection_key (live IA-trust
	// validated below, same as mint_cash/cash_transfer), or cash (caller
	// supplies their own commitment — see the cash-mode branch below for why).
	newIdentityType := params.NewIdentity.IdentityType
	if newIdentityType != db.CashIdentityPubkey && newIdentityType != db.CashIdentityConnectionKey && newIdentityType != db.CashIdentityCash {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			fmt.Sprintf("new_identity.identity_type must be %q, %q, or %q",
				db.CashIdentityPubkey, db.CashIdentityConnectionKey, db.CashIdentityCash))
		return
	}
	// Rate limit, charged only now that the request is a real attempt.
	//
	// Shares the claim limiter with cash_redeem/cash_transfer: a captured proof (or,
	// on those methods, a cash secret) is a presentation surface worth limiting even
	// without a signature to forge, and separate budgets would let an attacker split
	// attempts across methods to double their allowance.
	//
	// Charged after the shape checks above for the same reason as the other two
	// controllers: one envelope carries up to 32 items, each dispatched here
	// separately, so charging first let a single envelope of malformed requests spend
	// a shared bill's whole hourly allowance and lock out every other recipient.
	if !controller.cashClaimLimiter.Allow(app.AppPubkey, controller.cfg.GetEnv().CashWalletClaimRateLimitPerHour) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RATE_LIMITED, "rate limit exceeded for cash_consolidate")
		return
	}

	deps := controller.cashDeps()
	// Cash mode is validated separately from ValidateIdentityShape (which only
	// knows pubkey/connection_key — same split cash_transfer's own
	// new_identity validation already uses): the caller's own commitment MUST
	// be supplied here rather than generated and returned by the node — see
	// the cash-mode-target delivery note below for why — shaped as a 64-char
	// lowercase hex sha256, exactly like cash_transfer's cash-mode target.
	if newIdentityType == db.CashIdentityCash {
		if params.NewIdentity.IAPubkey != "" {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				"new_identity must not carry ia_pubkey when identity_type is cash")
			return
		}
		if decoded, decErr := hex.DecodeString(params.NewIdentity.IdentityValue); decErr != nil || len(decoded) != 32 {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
				"new_identity.identity_value is required for a cash-mode target and must be a 64-character lowercase "+
					"hex commitment (sha256 of a secret you generate and keep yourself) — the wallet never mints "+
					"or returns a cash secret over the shared connection")
			return
		}
	} else if err := cashwallet.ValidateIdentityShape(deps, newIdentityType, params.NewIdentity.IdentityValue, params.NewIdentity.IAPubkey); err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, err.Error())
		return
	}
	targetHash := newIdentityHash(newIdentityType, params.NewIdentity.IdentityValue, params.NewIdentity.IAPubkey)

	// 1. Resolve + authorize every source (read-only), before any claim. Reject
	// the whole request on the first failure so nothing is ever half-claimed.
	// Batch the custody lookup: one `wallet_pubkey IN (...)` query resolves every
	// source this node holds, instead of a round-trip per source (the query hits
	// idx_apps_pubkey_lookup). A pubkey absent from the map is simply not
	// custodied here and rejected per-source below.
	sourceWalletPubkeys := make([]string, len(params.Sources))
	for i := range params.Sources {
		sourceWalletPubkeys[i] = params.Sources[i].WalletPubkey
	}
	custodied, cErr := controller.loadCustodiedSources(sourceWalletPubkeys)
	if cErr != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to load source wallets")
		return
	}

	resolved := make([]resolvedConsolidateSource, 0, len(params.Sources))
	// Keyed on (wallet, identity), not wallet alone: NIP-CASH defines a
	// "source" as a distinct (wallet, identity) slice, and one wallet
	// routinely holds several slices at once (the ordinary state of a
	// freshly-minted multi-recipient wallet before any recipient has
	// acted) — see §Data Model, "a wallet's total funding MUST equal the
	// sum of its slices." Keying on the wallet alone incorrectly rejected
	// a spec-conformant consolidate of two distinct, individually-proven
	// slices that happen to share one wallet_pubkey.
	type seenKey struct {
		walletAppID   uint
		identityType  string
		identityValue string
	}
	seen := map[seenKey]bool{}
	var hubID *uint
	var total uint64
	var earliest *time.Time
	var minTransfer int64
	var redeemFee int
	var redeemFeeBase int64
	proofEventIDs := make([]string, 0, len(params.Sources))
	// iaTrustCache memoizes IsTrusted per ia_pubkey for this call only — see
	// resolveConsolidateSource's doc comment for why (TOCTOU: one consistent
	// trust snapshot across every connection_key source in the batch).
	iaTrustCache := make(map[string]bool)

	for i := range params.Sources {
		rs, evID, code, msg := controller.resolveConsolidateSource(params, i, targetHash, custodied, iaTrustCache)
		if code != "" {
			respondError(publishResponse, nip47Request.Method, code, fmt.Sprintf("source %d: %s", i, msg))
			return
		}
		key := seenKey{walletAppID: rs.walletApp.ID, identityType: rs.identityType, identityValue: rs.identityValue}
		if seen[key] {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "the same source slice appears twice")
			return
		}
		seen[key] = true

		// Same-hub (v1). Every source must descend from one Cash Hub.
		if rs.walletApp.ParentAppID == nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, fmt.Sprintf("source %d has no parent hub", i))
			return
		}
		if hubID == nil {
			hubID = rs.walletApp.ParentAppID
		} else if *hubID != *rs.walletApp.ParentAppID {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "all sources must belong to the same Cash Hub")
			return
		}

		// Merged terms: conservative bound on every inherited value
		// (§Consolidating Tokens). Expiry = earliest; min_transfer/redeem_fee
		// must agree across sources.
		amt := uint64(rs.claim.AmountMloki) //nolint:gosec // non-negative
		// Bounded by MaxInt64, not MaxUint64: Consolidate casts the final total
		// to int64 for CashWalletClaim.AmountMloki (a DB int64 column). The
		// hub-ceiling check below (PerWalletMaxMloki > 0 && total > ceiling)
		// already makes this cast safe today, since PerWalletMaxMloki is a Go
		// int validated strictly positive at both hub creation and update
		// (apps/cash_hub_service.go) — so any total that clears the ceiling is
		// necessarily <= that ceiling <= MaxInt64. This guard is defense-in-
		// depth against that invariant changing, not a currently-reachable
		// wrap: this codebase's established 0-means-unlimited convention
		// (already used for MaxExpSecs/MinTransferMloki/RedeemFeePpm) makes a
		// future "PerWalletMaxMloki == 0 means no ceiling" a plausible
		// addition this cast shouldn't depend on being caught elsewhere.
		// Mirrors cashwallet/create.go's identical guard on mint_cash's own
		// recipient-sum overflow.
		if amt > uint64(math.MaxInt64)-total {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "consolidated amount overflows")
			return
		}
		total += amt
		if len(resolved) == 0 {
			minTransfer = rs.claim.MinTransferMloki
			redeemFee = rs.claim.RedeemFeePpm
			redeemFeeBase = rs.claim.RedeemFeeBaseMloki
		} else {
			// The base joins this check because it is half of one quoted price. Merging
			// slices that agreed on the per-million part but not the flat part would
			// silently re-price whichever side lost, and the merged bill would quote a
			// fee its sources never agreed to.
			if rs.claim.MinTransferMloki != minTransfer || rs.claim.RedeemFeePpm != redeemFee ||
				rs.claim.RedeemFeeBaseMloki != redeemFeeBase {
				respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
					"sources disagree on min_transfer_millis/redeem_fee_ppm/redeem_fee_base; only same-terms slices may be consolidated")
				return
			}
		}
		// Conservative bound: the merged expiry is the earliest finite one
		// among the sources; a nil (never-expires) source contributes nothing,
		// so if every source is never, earliest stays nil (merged never).
		if rs.walletApp.ExpiresAt != nil {
			if earliest == nil || rs.walletApp.ExpiresAt.Before(*earliest) {
				earliest = rs.walletApp.ExpiresAt
			}
		}

		resolved = append(resolved, *rs)
		if evID != "" {
			proofEventIDs = append(proofEventIDs, evID)
		}
	}

	// 2. Hub-ceiling check on the merged total.
	hubApp := controller.appsService.GetAppById(*hubID)
	if hubApp == nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "could not load the sources' Cash Hub")
		return
	}
	hubConfig, err := controller.appsService.GetCashHubConfig(hubApp.ID)
	if err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "could not load the Cash Hub config")
		return
	}
	if hubConfig.PerWalletMaxMloki > 0 && total > uint64(hubConfig.PerWalletMaxMloki) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			fmt.Sprintf("consolidated amount %d exceeds the hub's per-wallet ceiling of %d", total, hubConfig.PerWalletMaxMloki))
		return
	}

	// 3. A claim proof is single-use (NIP-CASH §Claim Proofs). This read refuses a
	// proof already used; the commit consumes every proof, and only if it commits,
	// so a refused or failed call leaves them all usable.
	proofs := make([]cashwallet.ClaimProof, 0, len(proofEventIDs))
	for _, evID := range proofEventIDs {
		used, err := cashrequest.New(controller.db).IsProofConsumed(evID)
		if err != nil {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to check the source proofs")
			return
		}
		if used {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_UNAUTHORIZED, "a source proof was already used")
			return
		}
		// The proof's created_at is not kept here; the consumption time is a later,
		// so safely longer, retention anchor.
		proofs = append(proofs, cashwallet.ClaimProof{EventID: evID, CreatedAt: time.Now().Unix(), Method: nip47Request.Method})
	}

	// 4. One commit: every source claimed terminal (recording the merged bill it
	// moved into), the merged bill, its signed token, its funding from each
	// source and every consumed proof — or nothing at all.
	sources := make([]cashwallet.ConsolidateSource, 0, len(resolved))
	for _, rs := range resolved {
		sources = append(sources, cashwallet.ConsolidateSource{
			WalletApp: rs.walletApp,
			Slice: apps.SliceRef{ClaimID: rs.claim.ID, IdentityType: rs.identityType,
				IdentityValue: rs.identityValue, TransferCount: rs.claim.TransferCount},
			AmountMloki: uint64(rs.claim.AmountMloki), //nolint:gosec // non-negative
		})
	}
	result, err := cashwallet.Consolidate(ctx, deps, cashwallet.ConsolidateParams{
		HubApp:             hubApp,
		Sources:            sources,
		NewIdentityType:    params.NewIdentity.IdentityType,
		NewIdentityValue:   params.NewIdentity.IdentityValue,
		NewIAPubkey:        params.NewIdentity.IAPubkey,
		MinTransferMloki:   minTransfer,
		RedeemFeePpm:       redeemFee,
		RedeemFeeBaseMloki: redeemFeeBase,
		ExpiresAt:          earliest,
		Proofs:             proofs,
	})
	if err != nil {
		switch {
		case errors.Is(err, apps.ErrSliceClaimed):
			respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "a source slice was already claimed or is no longer available")
		case errors.Is(err, cashwallet.ErrProofUsed):
			respondError(publishResponse, nip47Request.Method, constants.ERROR_UNAUTHORIZED, "a source proof was already used")
		default:
			controller.respondCommitError(publishResponse, nip47Request.Method, app, err, "failed to consolidate slices")
		}
		return
	}

	// 6. Deliver the merged token. When new_identity is a pubkey, NIP-44
	// encrypted to the CALLER's own proof pubkey (resolved[0].proofPubkey) —
	// same convention as cash_transfer's spin-off delivery, and for the same
	// reason: this protects the token from a co-recipient of the calling
	// connection it travels back over, not from new_identity's own owner (a
	// pubkey-mode token isn't secret to begin with — redeeming one needs a
	// signature, not just the string; see the cash/connection_key case
	// below). The caller decrypts it themselves and hands it to new_identity
	// out of band, exactly like a cash_transfer gift. A cash-mode or
	// connection_key target has no real pubkey to ECDH against yet (cash-mode
	// never has one; connection_key doesn't until an Identity Authority
	// attests a real pubkey to it later), so the token travels in the clear
	// within the already end-to-end-encrypted outer NIP-47 response instead —
	// same as mint_cash's own cash/connection_key recipient delivery.
	// Funds have already moved either way, so a delivery failure is
	// operator-recoverable, never a rollback (the token is recoverable via
	// the admin API).
	newWalletPubkey := ""
	if result.WalletApp.WalletPubkey != nil {
		newWalletPubkey = *result.WalletApp.WalletPubkey
	}
	encryptedToken := result.CashToken
	if newIdentityType == db.CashIdentityPubkey {
		newWalletPrivKey, keyErr := controller.keys.GetAppWalletKey(result.WalletApp.ID)
		if keyErr != nil {
			logger.Logger.Error().Err(keyErr).Uint("new_wallet_id", result.WalletApp.ID).Msg("Consolidated but could not derive delivery key")
			respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "consolidated but its connection could not be delivered; contact the wallet operator")
			return
		}
		enc, encErr := encryptForRecipient(resolved[0].proofPubkey, newWalletPrivKey, result.CashToken)
		if encErr != nil {
			logger.Logger.Error().Err(encErr).Uint("new_wallet_id", result.WalletApp.ID).Msg("Consolidated but could not encrypt token")
			respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "consolidated but its connection could not be delivered; contact the wallet operator")
			return
		}
		encryptedToken = enc
	}

	var expiresAt *int64
	if result.WalletApp.ExpiresAt != nil {
		ts := result.WalletApp.ExpiresAt.Unix()
		expiresAt = &ts
	}
	logger.Logger.Info().
		Uint("new_wallet_id", result.WalletApp.ID).
		Int("source_count", len(sources)).
		Uint64("amount_mloki", result.AmountMloki).
		Msg("Cash wallet slices consolidated")

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result: cashConsolidateResponse{
			AmountMillis:    result.AmountMloki,
			NewWalletPubkey: newWalletPubkey,
			NewWalletToken:  encryptedToken,
			ExpiresAt:       expiresAt,
		},
	}, tags)

	// A consolidate drains its sources exactly as a full split does — their
	// value now lives in the new wallet — so each one that has nothing left
	// gets the same archive-and-delete treatment.
	//
	// Uniformity is the point, not tidiness. A bill the hub deleted is met
	// with silence; one it kept answers a request naming a spent slice with an
	// error. If consolidate alone left its sources behind, that error would
	// tell anyone who had ever seen those tokens that this hub issued them and
	// that they were spent (NIP-CASH §Lifecycle and Deletion).
	for _, source := range sources {
		controller.maybeAutoDeleteDrainedCashWallet(source.WalletApp)
	}
}

// loadCustodiedSources resolves every source's wallet_pubkey to the cash_wallet
// app this node holds, in a single query, returning a pubkey->app map. Bogus or
// non-custodied pubkeys are simply absent (rejected per-source by the caller).
func (controller *nip47Controller) loadCustodiedSources(sourceWalletPubkeys []string) (map[string]*db.App, error) {
	pubkeys := make([]string, 0, len(sourceWalletPubkeys))
	for _, pk := range sourceWalletPubkeys {
		if pk != "" {
			pubkeys = append(pubkeys, pk)
		}
	}
	custodied := make(map[string]*db.App, len(pubkeys))
	if len(pubkeys) == 0 {
		return custodied, nil
	}
	var apps []db.App
	if err := controller.db.Where("wallet_pubkey IN ? AND kind = ?", pubkeys, db.AppKindCashWallet).Find(&apps).Error; err != nil {
		return nil, err
	}
	for i := range apps {
		if apps[i].WalletPubkey != nil {
			custodied[*apps[i].WalletPubkey] = &apps[i]
		}
	}
	return custodied, nil
}

// resolveConsolidateSource locates a source wallet (from the pre-loaded custody
// map), verifies its pubkey or connection_key identity proof, and returns the
// resolved slice. code == "" means success; otherwise (code, msg) is the error
// to surface. evID is the proof's event id, to be replay-guarded by the caller
// once every source has passed. iaTrustCache memoizes IsTrusted per ia_pubkey
// across the whole batch (see HandleCashConsolidateEvent's call site) so every
// connection_key source in one request is checked against a single
// point-in-time trust snapshot, not a fresh live query each — closing a narrow
// TOCTOU window a per-source live re-check would otherwise leave open across a
// multi-source batch.
func (controller *nip47Controller) resolveConsolidateSource(params *nipcash.CashConsolidateRequest, i int, targetHash string, custodied map[string]*db.App, iaTrustCache map[string]bool) (rs *resolvedConsolidateSource, evID, code, msg string) {
	src := params.Sources[i]
	if src.WalletPubkey == "" {
		return nil, "", constants.ERROR_BAD_REQUEST, "wallet_pubkey is required"
	}
	// A cash_secret has no signature and no binding to the request that
	// carries it — presenting it just IS the authorization. Unlike
	// cash_transfer/cash_redeem (which never name a foreign wallet_pubkey and
	// always act on the calling connection's own app, so a cash secret only
	// ever transits over its own single-recipient wallet's connection),
	// cash_consolidate lets a source name ANY wallet this node custodies. If
	// that source were a cash-mode wallet OTHER than the caller's own connection,
	// its secret would sit in plaintext in a request encrypted only under the
	// CALLING connection's shared key — decryptable by every co-recipient of a
	// multi-identity calling wallet, none of whom have any claim on that
	// foreign cash note. Rejected outright rather than scoped to
	// "self only": unlike connection_key (a signed proof, immune to this),
	// cash mode genuinely needs a new bound-proof protocol addition to be safe as
	// a source — see the design doc; still deferred.
	if src.CashSecret != "" {
		return nil, "", constants.ERROR_BAD_REQUEST, "cash-mode sources are not supported by cash_consolidate yet"
	}

	// Custody: the source MUST be a cash_wallet this node itself issued.
	walletApp, ok := custodied[src.WalletPubkey]
	if !ok {
		return nil, "", constants.ERROR_NOT_FOUND, "no cash_wallet this node custodies matches wallet_pubkey"
	}

	if src.IdentityType != db.CashIdentityPubkey && src.IdentityType != db.CashIdentityConnectionKey {
		return nil, "", constants.ERROR_BAD_REQUEST, "identity_type must be pubkey or connection_key"
	}
	if src.IdentityValue == "" || src.IdentityEvent == "" {
		return nil, "", constants.ERROR_BAD_REQUEST, "identity_type, identity_value, and identity_event are required"
	}
	if src.IdentityType == db.CashIdentityConnectionKey && src.AttestationEvent == "" {
		return nil, "", constants.ERROR_BAD_REQUEST, "attestation_event is required when identity_type is connection_key"
	}
	identityType := src.IdentityType
	identityValue := src.IdentityValue

	claim, err := controller.appsService.GetCashWalletClaim(walletApp.ID, identityType, identityValue)
	if err != nil {
		return nil, "", constants.ERROR_INTERNAL, "failed to look up the source slice"
	}
	if claim == nil {
		return nil, "", constants.ERROR_NOT_FOUND, "no unclaimed slice registered for this identity on the source"
	}

	var identityEvent nostr.Event
	if err := json.Unmarshal([]byte(src.IdentityEvent), &identityEvent); err != nil {
		return nil, "", constants.ERROR_BAD_REQUEST, "identity_event is not valid JSON"
	}

	var attestationEvent nostr.Event
	attestationEventID := ""
	if identityType == db.CashIdentityConnectionKey {
		if err := json.Unmarshal([]byte(src.AttestationEvent), &attestationEvent); err != nil {
			return nil, "", constants.ERROR_BAD_REQUEST, "attestation_event is not valid JSON"
		}
		attestationEventID = attestationEvent.ID
	}

	// The proof binds to the source's own wallet pubkey (d-tag), the merged
	// new_identity (targetHash), and the slice's full amount — a consolidate
	// consumes each source whole.
	fullAmount := uint64(claim.AmountMloki) //nolint:gosec // non-negative
	if err := verifyTransferIdentityEvent(&identityEvent, identityType, identityValue, src.WalletPubkey, targetHash, fullAmount, attestationEventID); err != nil {
		return nil, "", constants.ERROR_BAD_REQUEST, err.Error()
	}

	if identityType == db.CashIdentityConnectionKey {
		trusted, cached := iaTrustCache[claim.IAPubkey]
		if !cached {
			trusted, err = controller.iaChecker.IsTrusted(claim.IAPubkey)
			if err != nil {
				return nil, "", constants.ERROR_INTERNAL, "failed to check Identity Authority trust"
			}
			iaTrustCache[claim.IAPubkey] = trusted
		}
		if !trusted {
			return nil, "", constants.ERROR_RESTRICTED, "the Identity Authority for this source has been revoked"
		}
		if err := VerifyClaimAttestationEvent(&attestationEvent, claim.IAPubkey, identityEvent.PubKey, identityValue); err != nil {
			return nil, "", constants.ERROR_BAD_REQUEST, err.Error()
		}
	}
	evID = identityEvent.ID

	return &resolvedConsolidateSource{
		walletApp:     walletApp,
		identityType:  identityType,
		identityValue: identityValue,
		claim:         claim,
		proofPubkey:   identityEvent.PubKey,
	}, evID, "", ""
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
// window. Callers MUST additionally insert a db.CashConsumedProof row (see
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
