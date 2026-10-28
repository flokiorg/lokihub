package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flokiorg/lokihub/hubclock"
	"math"
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
// maxConsolidateSourcesCap mirrors mint_cash's/the hub config's own
// maxRecipientsPerWallet=100 cap (cashwallet/create.go, apps/cash_hub_service.go)
// — the same "cap every multi-item Cash batch" convention, applied here since
// this feature shipped without it. Without a ceiling, one rate-limited
// cash_consolidate request could bundle an arbitrary number of independent
// custody lookups and proof verifications in one call.
const maxConsolidateSourcesCap = 100

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
	RequestID   string                       `json:"request_id,omitempty"`
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

// HandleCashConsolidateEvent combines several same-hub slices this node holds
// into one new bill (NIP-CASH §Consolidating Tokens), in the spec's order:
//
//  1. the item's target is sources[0]; at least two sources, none repeated, none
//     cash-mode (BAD_REQUEST); at most the announced cap (QUOTA_EXCEEDED)
//  2. every source's claim proof, examined alike; any failure of any kind is
//     one UNAUTHORIZED; the item proof is sources[0]'s claim signer's; then the
//     request_id is marked
//  3. every source's state: claimed or expired, another Hub, an untrusted IA
//     (RESTRICTED)
//  4. new_identity (BAD_REQUEST, untrusted IA RESTRICTED)
//  5. an overflow-checked sum within the Hub's ceiling (QUOTA_EXCEEDED)
//  6. the earliest expiry, identical terms (RESTRICTED), the live-bill cap
//     (QUOTA_EXCEEDED)
//  7. one commit: every source claimed, the merged bill, its funding, every
//     proof consumed, the record
//  8. delivery: nested-encrypted to sources[0]'s signer for a pubkey
//     new_identity, in the clear for connection_key or cash
//
// A cash-mode source is not accepted: its secret would sit in a request
// encrypted to the calling bill, readable by that bill's co-recipients.
func (controller *nip47Controller) HandleCashConsolidateEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, tags nostr.Tags, call *PrivateCall) {
	params := &nipcash.CashConsolidateRequest{}
	if resp := decodeRequest(nip47Request, params); resp != nil {
		publishResponse(resp, tags)
		return
	}
	method := nip47Request.Method
	refuse := func(code, message string) { respondError(publishResponse, method, code, message) }

	// The private loop refused a missing, malformed or used request_id at I6 and
	// I7; this is the same refusal for a caller that reached here another way.
	if call == nil || !call.Meta.DeadlinePresent {
		refuse(constants.ERROR_BAD_REQUEST, "cash_consolidate needs a deadline")
		return
	}
	if !nipcash.ValidRequestID(params.RequestID) {
		refuse(constants.ERROR_BAD_REQUEST, "request_id is required: 32 to 64 lowercase hex characters")
		return
	}
	if app.Kind != db.AppKindCashWallet {
		refuse(constants.ERROR_RESTRICTED, "cash_consolidate requires a cash_wallet app")
		return
	}

	logger.Logger.Info().Uint("app_id", app.ID).Int("source_count", len(params.Sources)).Msg("Handling cash_consolidate request")

	// Step 1.
	if len(params.Sources) == 0 || app.WalletPubkey == nil || params.Sources[0].WalletPubkey != *app.WalletPubkey {
		refuse(constants.ERROR_BAD_REQUEST, "the item's target must be the first source's wallet_pubkey")
		return
	}
	if len(params.Sources) < 2 {
		refuse(constants.ERROR_BAD_REQUEST, "consolidate requires at least two sources")
		return
	}
	type sliceKey struct{ wallet, identityType, identityValue string }
	seen := make(map[sliceKey]bool, len(params.Sources))
	for _, src := range params.Sources {
		key := sliceKey{src.WalletPubkey, src.IdentityType, src.IdentityValue}
		if seen[key] {
			refuse(constants.ERROR_BAD_REQUEST, "the same source slice appears twice")
			return
		}
		seen[key] = true
		if src.CashSecret != "" {
			refuse(constants.ERROR_BAD_REQUEST, "cash-mode sources are not accepted by cash_consolidate")
			return
		}
	}
	if limit := controller.maxConsolidateSources(); len(params.Sources) > limit {
		refuse(constants.ERROR_QUOTA_EXCEEDED, fmt.Sprintf("at most %d sources per consolidate, got %d", limit, len(params.Sources)))
		return
	}

	// Step 2: every source's proof, before any other check on any source.
	sources, ok := controller.authorizeConsolidateSources(params, call.ItemSigner)
	if !ok {
		refuse(constants.ERROR_UNAUTHORIZED, consolidateUnauthorized)
		return
	}
	// The call limit, keyed by the verified signer (sources[0]'s, which signed the
	// item proof) and shared with cash_redeem and cash_transfer.
	if !controller.claimCallAllowed(call.ItemSigner) {
		refuse(constants.ERROR_RATE_LIMITED, "too many calls by this signer; retry later")
		return
	}
	if err := cashrequest.New(controller.db).MarkUsed(params.RequestID, method); err != nil {
		if errors.Is(err, cashrequest.ErrRequestReused) {
			refuse(constants.ERROR_REQUEST_REUSED, "this request_id was already used on this node")
			return
		}
		refuse(constants.ERROR_INTERNAL, "could not record this request_id, please retry")
		return
	}

	// Step 3: each source's state.
	hubID := sources[0].bill.ParentAppID
	now := hubclock.Now()
	for i, s := range sources {
		switch {
		case s.claim.ClaimedAt != nil:
			refuse(constants.ERROR_RESTRICTED, fmt.Sprintf("source %d was already claimed or is no longer available", i))
			return
		case s.bill.ExpiresAt != nil && now.After(*s.bill.ExpiresAt):
			refuse(constants.ERROR_RESTRICTED, fmt.Sprintf("source %d is past its expiry", i))
			return
		case s.bill.ParentAppID == nil || hubID == nil || *s.bill.ParentAppID != *hubID:
			refuse(constants.ERROR_RESTRICTED, "all sources must belong to the same Cash Hub")
			return
		case s.iaUntrusted:
			refuse(constants.ERROR_RESTRICTED, fmt.Sprintf("the Identity Authority of source %d is no longer trusted", i))
			return
		}
	}

	// Step 4: new_identity, validated as cash_transfer validates it.
	newType, newValue, newIA := params.NewIdentity.IdentityType, params.NewIdentity.IdentityValue, params.NewIdentity.IAPubkey
	if code, message := validateNewIdentity(controller.iaChecker, newType, newValue, newIA); code != "" {
		refuse(code, message)
		return
	}

	// Step 5: the sum, within the Hub's per-wallet ceiling.
	hubApp := controller.appsService.GetAppById(*hubID)
	if hubApp == nil {
		refuse(constants.ERROR_INTERNAL, "could not load the sources' Cash Hub")
		return
	}
	hubConfig, err := controller.appsService.GetCashHubConfig(hubApp.ID)
	if err != nil {
		refuse(constants.ERROR_INTERNAL, "could not load the Cash Hub config")
		return
	}
	var total uint64
	for _, s := range sources {
		amt := uint64(s.claim.AmountMloki) //nolint:gosec // non-negative
		if amt > uint64(math.MaxInt64)-total {
			refuse(constants.ERROR_BAD_REQUEST, "consolidated amount overflows")
			return
		}
		total += amt
	}
	if hubConfig.PerWalletMaxMloki > 0 && total > uint64(hubConfig.PerWalletMaxMloki) {
		refuse(constants.ERROR_QUOTA_EXCEEDED,
			fmt.Sprintf("consolidated amount %d exceeds the hub's per-wallet ceiling of %d", total, hubConfig.PerWalletMaxMloki))
		return
	}

	// Step 6: the merged terms. The earliest finite expiry; a never-expiring
	// source contributes nothing. The floor and both fee parts are one set of
	// terms and must agree, or the merge would re-price whichever side lost.
	var earliest *time.Time
	first := sources[0].claim
	for _, s := range sources {
		if s.claim.MinTransferMloki != first.MinTransferMloki || s.claim.RedeemFeePpm != first.RedeemFeePpm ||
			s.claim.RedeemFeeBaseMloki != first.RedeemFeeBaseMloki {
			refuse(constants.ERROR_RESTRICTED,
				"sources disagree on min_transfer_millis/redeem_fee_ppm/redeem_fee_base; only same-terms slices may be consolidated")
			return
		}
		// Ordered and not-ordered sources are never merged (NIP-CASH §Consolidating
		// Tokens); a merge of ordered sources is ordered.
		if s.claim.Ordered != first.Ordered {
			refuse(constants.ERROR_RESTRICTED, "ordered and not-ordered slices cannot be consolidated together")
			return
		}
		if e := s.bill.ExpiresAt; e != nil && (earliest == nil || e.Before(*earliest)) {
			earliest = e
		}
	}
	// A merge is bounded by the live-bill cap only: it ends more bills than it
	// makes, so it spends no lineage's spin-off allowance.
	if code, message := controller.spinOffLimitRefusal(sources[0].bill, 1, false); code != "" {
		refuse(code, message)
		return
	}

	// Step 7: one commit.
	signer := sources[0].signer
	var delivery *string
	if newType == db.CashIdentityPubkey {
		delivery = &signer
	}
	meta := cashConsolidateResponse{AmountMillis: total}
	if earliest != nil {
		at := earliest.Unix()
		meta.ExpiresAt = &at
	}
	resultMeta, err := json.Marshal(struct {
		AmountMillis uint64 `json:"amount_millis"`
		ExpiresAt    *int64 `json:"expires_at,omitempty"`
	}{meta.AmountMillis, meta.ExpiresAt})
	if err != nil {
		refuse(constants.ERROR_INTERNAL, "could not record this call")
		return
	}
	record := &db.CashRequestRecord{
		RequestID:          params.RequestID,
		Method:             method,
		HubAppID:           hubApp.ID,
		TargetWalletPubkey: *app.WalletPubkey,
		TargetConnPubkey:   app.AppPubkey,
		AuthorizerKind:     "pubkey",
		Authorizer:         signer,
		Deadline:           call.Meta.Deadline,
		DeliveryPubkey:     delivery,
		ResultMeta:         string(resultMeta),
	}
	inputs := make([]cashwallet.ConsolidateSource, len(sources))
	proofs := make([]cashwallet.ClaimProof, len(sources))
	for i, s := range sources {
		inputs[i] = cashwallet.ConsolidateSource{
			WalletApp: s.bill,
			Slice: apps.SliceRef{ClaimID: s.claim.ID, IdentityType: s.claim.IdentityType,
				IdentityValue: s.claim.IdentityValue, TransferCount: s.claim.TransferCount},
			AmountMloki: uint64(s.claim.AmountMloki), //nolint:gosec // non-negative
		}
		proofs[i] = cashwallet.ClaimProof{EventID: s.proofID, CreatedAt: s.proofCreatedAt, Method: method}
	}
	unfinal := make([]*string, len(sources))
	for i, s := range sources {
		unfinal[i] = s.claim.UnfinalPaymentIDs
	}
	unfinalIDs, err := db.MergePaymentIDs(unfinal...)
	if err != nil {
		refuse(constants.ERROR_INTERNAL, "could not read the sources' order payments")
		return
	}
	result, err := cashwallet.Consolidate(ctx, controller.cashDeps(), cashwallet.ConsolidateParams{
		HubApp:             hubApp,
		Sources:            inputs,
		NewIdentityType:    newType,
		NewIdentityValue:   newValue,
		NewIAPubkey:        newIA,
		MinTransferMloki:   first.MinTransferMloki,
		RedeemFeePpm:       first.RedeemFeePpm,
		RedeemFeeBaseMloki: first.RedeemFeeBaseMloki,
		Ordered:            first.Ordered,
		UnfinalPaymentIDs:  unfinalIDs,
		ExpiresAt:          earliest,
		Proofs:             proofs,
		Record:             record,
	})
	if err != nil {
		switch {
		case errors.Is(err, apps.ErrSliceClaimed):
			refuse(constants.ERROR_RESTRICTED, "a source slice was already claimed or is no longer available")
		case errors.Is(err, cashwallet.ErrProofUsed):
			refuse(constants.ERROR_UNAUTHORIZED, consolidateUnauthorized)
		default:
			controller.respondCommitError(publishResponse, method, app, err, "failed to consolidate slices")
		}
		return
	}

	// Step 8: deliver. A pubkey new_identity's token is nested-encrypted to the
	// caller (sources[0]'s signer), who hands it on, so no co-recipient of the
	// calling bill reads it. connection_key and cash targets have no pubkey to
	// encrypt to yet and travel in the clear inside the outer encryption, as a
	// mint's do. The bill is committed either way, so a delivery failure is
	// recoverable by the operator, never a rollback.
	newWalletPubkey := ""
	if result.WalletApp.WalletPubkey != nil {
		newWalletPubkey = *result.WalletApp.WalletPubkey
	}
	token := result.CashToken
	if newType == db.CashIdentityPubkey {
		newWalletPrivKey, keyErr := controller.keys.GetAppWalletKey(result.WalletApp.ID)
		if keyErr != nil {
			logger.Logger.Error().Err(keyErr).Uint("new_wallet_id", result.WalletApp.ID).Msg("Consolidated but could not derive delivery key")
			refuse(constants.ERROR_INTERNAL, "consolidated but its connection could not be delivered; contact the wallet operator")
			return
		}
		enc, encErr := encryptForRecipient(signer, newWalletPrivKey, result.CashToken)
		if encErr != nil {
			logger.Logger.Error().Err(encErr).Uint("new_wallet_id", result.WalletApp.ID).Msg("Consolidated but could not encrypt token")
			refuse(constants.ERROR_INTERNAL, "consolidated but its connection could not be delivered; contact the wallet operator")
			return
		}
		token = enc
	}

	logger.Logger.Info().
		Uint("new_wallet_id", result.WalletApp.ID).
		Int("source_count", len(sources)).
		Uint64("amount_mloki", result.AmountMloki).
		Msg("Cash wallet slices consolidated")

	publishResponse(&models.Response{
		ResultType: method,
		Result: cashConsolidateResponse{
			AmountMillis:    result.AmountMloki,
			NewWalletPubkey: newWalletPubkey,
			NewWalletToken:  token,
			ExpiresAt:       meta.ExpiresAt,
		},
	}, tags)

	// The sources are drained, so each one that holds nothing more gets the same
	// archive-and-delete every drain mechanism gets (NIP-CASH §Lifecycle and
	// Deletion): a deleted bill is met with silence, a kept one would answer that
	// its slice was spent.
	for _, s := range sources {
		controller.maybeAutoDeleteDrainedCashWallet(s.bill)
	}
}

// consolidateUnauthorized is the one refusal for every step-2 failure, whatever
// it is (NIP-CASH §Consolidating Tokens, step 2): a source not held, no slice for
// its identity, a proof or attestation that fails.
const consolidateUnauthorized = "sources not authorized"

// authorizedSource is a source whose claim proof verified.
type authorizedSource struct {
	bill           *db.App
	claim          *db.CashWalletClaim
	signer         string
	proofID        string
	proofCreatedAt int64
	// iaUntrusted: the attestation holds but its IA is off the allowlist, which
	// step 3 refuses with RESTRICTED once every proof verified.
	iaUntrusted bool
}

// authorizeConsolidateSources verifies every source's claim proof (NIP-CASH
// §Consolidating Tokens, step 2) against the slice its identity holds on the
// bill named, bound to m cash_consolidate, that slice's committed amount,
// new_identity and this call's source list. Every source is examined the same
// way, whatever an earlier one showed, and the result is one yes or no; the item
// proof must be sources[0]'s claim signer's.
func (controller *nip47Controller) authorizeConsolidateSources(params *nipcash.CashConsolidateRequest, itemSigner string) ([]authorizedSource, bool) {
	pubkeys := make([]string, len(params.Sources))
	for i, s := range params.Sources {
		pubkeys[i] = s.WalletPubkey
	}
	// A malformed wallet_pubkey makes no hash, so every proof fails below.
	sourcesHash, _ := nipcash.SourcesHash(pubkeys)
	held, err := controller.loadCustodiedSources(pubkeys)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to load consolidate sources")
		return nil, false
	}
	newIdentityHash := nipcash.NewIdentityHashOf(params.NewIdentity.IdentityType, params.NewIdentity.IdentityValue, params.NewIdentity.IAPubkey)
	// One trust snapshot for the whole call.
	trust := map[string]bool{}
	iaTrusted := func(ia string) bool {
		if v, ok := trust[ia]; ok {
			return v
		}
		v, err := controller.iaChecker.IsTrusted(ia)
		v = err == nil && v
		trust[ia] = v
		return v
	}
	store := cashrequest.New(controller.db)

	out := make([]authorizedSource, len(params.Sources))
	ok := true
	for i, src := range params.Sources {
		bill := held[src.WalletPubkey]
		claim := &db.CashWalletClaim{}
		if bill != nil {
			// The slice this identity holds, claimed or not: its state is step 3's.
			// Its live slice comes first; a claimed one is history it may also have.
			if err := controller.db.Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ?",
				bill.ID, src.IdentityType, src.IdentityValue).
				Order("CASE WHEN claimed_at IS NULL THEN 0 ELSE 1 END").Order("id DESC").Limit(1).Find(claim).Error; err != nil {
				claim = &db.CashWalletClaim{}
			}
		}
		amount := uint64(max(claim.AmountMloki, 0)) //nolint:gosec // non-negative
		verdict, err := nipcash.VerifyClaimProof(src.IdentityEvent, nipcash.ClaimExpectation{
			Binding: nipcash.ClaimBinding{
				Method:          constants.NIP47MethodCashConsolidate,
				Wallet:          src.WalletPubkey,
				NewIdentityHash: newIdentityHash,
				AmountMillis:    &amount,
				SourcesHash:     sourcesHash,
			},
			SliceType:   src.IdentityType,
			SliceValue:  src.IdentityValue,
			SliceIA:     claim.IAPubkey,
			Attestation: src.AttestationEvent,
			IATrusted:   iaTrusted,
			Consumed: func(id string) bool {
				used, err := store.IsProofConsumed(id)
				return err == nil && used
			},
		}, time.Now())
		var claimErr *nipcash.ClaimError
		untrusted := err != nil && errors.As(err, &claimErr) && claimErr.Code == constants.ERROR_RESTRICTED
		if (err != nil && !untrusted) || bill == nil || claim.ID == 0 {
			ok = false
			continue
		}
		signer := verdict.Signer
		if untrusted {
			signer = proofSigner(src.IdentityEvent)
		}
		out[i] = authorizedSource{bill: bill, claim: claim, signer: signer, proofID: proofID(src.IdentityEvent, verdict),
			proofCreatedAt: eventCreatedAt(src.IdentityEvent), iaUntrusted: untrusted}
	}
	if !ok || itemSigner == "" || itemSigner != out[0].signer {
		return nil, false
	}
	return out, true
}

// proofSigner and proofID read a claim proof whose only failure was its IA's
// trust, so its id and signature verified.
func proofSigner(raw string) string {
	var ev struct {
		PubKey string `json:"pubkey"`
	}
	_ = json.Unmarshal([]byte(raw), &ev)
	return ev.PubKey
}

func proofID(raw string, v nipcash.ClaimVerdict) string {
	if v.RecomputedID != "" {
		return v.RecomputedID
	}
	var ev struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(raw), &ev)
	return ev.ID
}

// maxConsolidateSources is the announced max_consolidate_sources, at most 100.
func (controller *nip47Controller) maxConsolidateSources() int {
	limit := controller.cfg.PrivateEnvelopeLimits().MaxConsolidateSources
	if limit <= 0 || limit > maxConsolidateSourcesCap {
		return maxConsolidateSourcesCap
	}
	return limit
}

// validateNewIdentity validates a transfer's or consolidate's new_identity: a
// pubkey or connection_key as a mint recipient (an untrusted IA RESTRICTED), or
// cash as the caller's own commitment, never generated here. "" when valid.
func validateNewIdentity(ia cashwallet.IATrustChecker, identityType, identityValue, iaPubkey string) (string, string) {
	switch identityType {
	case db.CashIdentityCash:
		if iaPubkey != "" {
			return constants.ERROR_BAD_REQUEST, "new_identity must not carry ia_pubkey when identity_type is cash"
		}
		if !nipcash.ValidCommitment(identityValue) {
			return constants.ERROR_BAD_REQUEST, "new_identity.identity_value is required for a cash-mode target and must be a 64-character " +
				"lowercase hex commitment (sha256 of a secret you generate and keep yourself); the hub never mints or returns a cash secret"
		}
		return "", ""
	case db.CashIdentityPubkey, db.CashIdentityConnectionKey:
		if err := cashwallet.ValidateIdentityShape(cashwallet.Deps{IAChecker: ia}, identityType, identityValue, iaPubkey); err != nil {
			if errors.Is(err, cashwallet.ErrRestricted) {
				return constants.ERROR_RESTRICTED, err.Error()
			}
			return constants.ERROR_BAD_REQUEST, err.Error()
		}
		return "", ""
	default:
		return constants.ERROR_BAD_REQUEST, fmt.Sprintf("new_identity.identity_type must be %q, %q, or %q",
			db.CashIdentityPubkey, db.CashIdentityConnectionKey, db.CashIdentityCash)
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
