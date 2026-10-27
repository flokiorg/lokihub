// Package cashwallet holds the protocol-agnostic core of "create a shared Cash
// wallet under a cash_hub": validate every recipient, spin up one spend-only
// child app serving all of them, and fund it via a single internal transfer
// from the hub. Both the NIP-47 mint_cash controller and the admin
// HTTP API call into this package, so the funding/rollback logic lives in
// exactly one place.
//
// A Cash wallet's connection is meant to be shared/handed out freely among its
// recipients — knowing the connection alone never lets you spend, because
// cash_redeem (see nip47/controllers/cash_redeem_controller.go) requires each
// recipient to separately prove which identity they are before paying out
// their own slice. This package only concerns itself with creating that
// shared wallet and its recipient slices; the proof-gated payout lives in the
// controller.
//
// This package deliberately knows nothing about NIP-47 (rate limiting) or
// HTTP — those are protocol concerns that stay in their callers.
package cashwallet

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/transactions"
	"github.com/ohstr/nmilat/nipcash"
	"gorm.io/gorm"
)

// activeHubCommits serializes concurrent mint_cash attempts against
// the same cash_hub — across BOTH the NWC path
// (nip47/controllers/mint_cash_controller.go) and the admin HTTP path
// (api.CreateCashWallet) — so Resolve's balance pre-check and Commit's actual
// fund transfer can't race a second, concurrent creation past a stale
// balance read. Lives here (package-level, not on either caller) because
// both callers must share the same lock/key space; app IDs are globally
// unique across every app kind in the single `apps` table, so keying on
// hubAppID alone is safe. Package-level, not per controller: production
// builds a new controller per request, so a controller-local guard would
// never see a second request.
var activeHubCommits sync.Map // map[uint]struct{}

// LockHub attempts to acquire the in-process creation slot for hubAppID. ok
// is false if another mint_cash call for this hub is already in
// flight; the caller should reject the request rather than block. Callers
// should defer release() immediately
// after a successful acquire, once Resolve and Commit have both finished (or
// failed).
func LockHub(hubAppID uint) (release func(), ok bool) {
	if _, loaded := activeHubCommits.LoadOrStore(hubAppID, struct{}{}); loaded {
		return nil, false
	}
	return func() { activeHubCommits.Delete(hubAppID) }, true
}

// IATrustChecker reports whether a pubkey is a registered, trusted Identity
// Authority. Satisfied by *apps.IdentityAuthorityManager; declared as an
// interface here (mirroring apps.AppsService/transactions.TransactionsService)
// so callers can substitute a fake in tests.
type IATrustChecker interface {
	IsTrusted(pubkey string) (bool, error)
}

// Deps are the services cashwallet.Create needs. Callers construct this from
// their own already-wired instances (nip47Controller's fields, or api's).
type Deps struct {
	AppsService         apps.AppsService
	TransactionsService transactions.TransactionsService
	LNClient            lnclient.LNClient
	Keys                keys.Keys
	DB                  *gorm.DB
	// RelayURLs is used to build the pairing URI.
	RelayURLs []string
	// IAChecker enforces the Identity Authority allowlist for connection_key-mode
	// recipients. Only consulted when a recipient's IdentityType is
	// db.CashIdentityConnectionKey, so callers that only ever create
	// pubkey-mode wallets may leave it nil.
	IAChecker IATrustChecker
	// Hooks is the commit's fault-injection seam (tests only; nil in production).
	Hooks *TestHooks
	// Now is the clock the commit checks its deadline against; nil is time.Now.
	Now func() time.Time
}

// RecipientInput describes one recipient's requested slice of a shared Cash
// wallet. IAPubkey is only meaningful when IdentityType is
// db.CashIdentityConnectionKey. For db.CashIdentityCash, IdentityValue is the
// sha256 commitment of a secret the caller generated (NIP-CASH §Minting Cash);
// the Hub never sees the secret.
type RecipientInput struct {
	IdentityType  string // db.CashIdentityPubkey | db.CashIdentityConnectionKey | db.CashIdentityCash
	IdentityValue string
	IAPubkey      string
	AmountMloki   uint64
}

// Params describes the shared wallet to create. MinTransferMloki is
// deliberately not a field here: it's a Cash-Hub-level setting
// (db.CashHubConfig.MinTransferMloki) Resolve reads directly, not a
// per-call value a caller supplies — see Resolved.MinTransferMloki.
type Params struct {
	HubApp     *db.App
	Recipients []RecipientInput
	ExpirySecs int
	// RefusedField names a field the request carried that mint_cash refuses
	// whatever its value (a recipient's cash_secret, idempotency_key,
	// min_transfer_millis), or "" (NIP-CASH §Minting Cash, step 4). Resolve
	// refuses it at its place in the processing order.
	RefusedField string
}

// ErrRestricted: Hub policy refuses the request (NIP-CASH RESTRICTED). It wraps
// constants.ErrInvalidParams, so a caller that only knows that error still
// treats it as the caller's fault.
var ErrRestricted = fmt.Errorf("%w: refused by this Hub's policy", constants.ErrInvalidParams)

// RecipientResult echoes back one recipient's committed slice; a cash-mode entry
// echoes the caller's commitment.
type RecipientResult struct {
	IdentityType  string
	IdentityValue string
	IAPubkey      string
	AmountMloki   uint64
}

// Result carries everything a caller needs to build its own protocol-specific
// response. CashToken alone is the bill's credential (NIP-CASH §Minting Cash).
type Result struct {
	WalletApp *db.App
	CashToken string
	// ExpiresAt is nil when this wallet never expires — the Cash Hub's own
	// MaxExpSecs is 0 ("never") and the caller didn't request its own
	// expiry. See Resolved.ExpiresAt.
	ExpiresAt  *time.Time
	Recipients []RecipientResult
}

// Resolved is the outcome of Resolve: every read-only check (identity shape,
// IA trust, expiry/amount caps, hub balance) has already passed, and these
// are the exact values Commit will act on. Splitting Create into
// Resolve+Commit lets a caller insert a rate-limit check in between, so a
// request that was always going to fail validation never consumes rate-limit
// quota (mirroring create_circle_wallet_controller.go).
type Resolved struct {
	HubApp *db.App
	// Recipients: validated, exactly as the caller sent them.
	Recipients []RecipientInput
	// ExpiresAt is nil when the wallet being created will never expire —
	// only possible when the Hub's own MaxExpSecs is 0 ("never") and the
	// caller's own ExpirySecs was omitted/zero too. See Resolve's
	// expiry-resolution comment for the full rule; Commit passes this
	// straight through to db.App.ExpiresAt (already nilable, and already
	// treated as "never swept"/"never expired" everywhere it's read —
	// service.runCashCleanup's `expires_at < ?` query and
	// api.cashClaimStatus both already skip a nil ExpiresAt correctly).
	ExpiresAt *time.Time
	// MinTransferMloki is the Cash Hub's own configured default floor (0 = no
	// floor), applied uniformly to every recipient's claim row. A slice
	// created by a later Split inherits its OWN MinTransferMloki from the
	// specific slice it was split from, not freshly from this hub config
	// each time (see Split/SplitParams).
	MinTransferMloki int64
	// RedeemFeePpm is the Cash Hub's own configured default cash_redeem fee
	// (0 = free), applied uniformly to every recipient's claim row — same
	// inheritance posture as MinTransferMloki: a slice created by a later
	// Split inherits its OWN RedeemFeePpm from the specific slice it was
	// split from, never freshly from this hub config.
	RedeemFeePpm int
	// RedeemFeeBaseMloki is the flat part of the same quoted fee, carried under the
	// same rules — the two are one price and must never travel separately.
	RedeemFeeBaseMloki int64
}

// maxRecipientsPerWallet mirrors apps.maxRecipientsPerWallet — duplicated as
// a small local constant since apps doesn't export its own (kept private to
// avoid the two packages needing to agree on an exported name for a single
// shared limit that's re-validated at insert time anyway).
const maxRecipientsPerWallet = 100

// Resolve performs every read-only check of a mint, in NIP-CASH's order
// (§Minting Cash, Processing Algorithm, steps 3 to 6; the caller has done steps
// 1 and 2), without creating or funding anything:
//
//  3. recipients: at least one, at most maxRecipientsPerWallet
//     (QUOTA_EXCEEDED), each well formed, none twice, a cash entry alone;
//     amounts positive, each within the per-wallet ceiling before it is added
//     (QUOTA_EXCEEDED), an overflow-checked sum (BAD_REQUEST) within the
//     ceiling (QUOTA_EXCEEDED)
//  4. every connection_key IA trusted now (RESTRICTED), every cash commitment
//     well formed, no refused field (BAD_REQUEST)
//  5. expiry: over a finite ceiling RESTRICTED, over the representational
//     bound BAD_REQUEST
//  6. the Hub's balance covers the sum (INSUFFICIENT_BALANCE)
//
// It's the counterpart to Commit; Create is Resolve then Commit.
func Resolve(ctx context.Context, deps Deps, params Params) (*Resolved, error) {
	if params.HubApp.Kind != db.AppKindCashHub {
		return nil, fmt.Errorf("%w: mint_cash requires a cash_hub app", constants.ErrInvalidParams)
	}
	// Refuse to mint before spending any balance, rather than hand back a bill
	// nobody can reach. A Cash Wallet is contacted only through the relays
	// embedded in its own token — there is no discovery path and no client-side
	// fallback, so a bill minted with no relay is permanently unspendable
	// through the string its holder was given. config.GetRelayUrls already
	// strips empty entries, so an unset Relay config arrives here as an empty
	// list rather than as one relay named "".
	if len(deps.RelayURLs) == 0 {
		return nil, fmt.Errorf("%w: this hub has no relay configured, so a minted bill would be unreachable",
			constants.ErrInvalidParams)
	}

	hubConfig, err := deps.AppsService.GetCashHubConfig(params.HubApp.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load Cash Hub config: %w", err)
	}

	// Step 3: every recipient.
	if len(params.Recipients) == 0 {
		return nil, fmt.Errorf("%w: recipients list is empty", constants.ErrInvalidParams)
	}
	if len(params.Recipients) > maxRecipientsPerWallet {
		return nil, fmt.Errorf("%w: at most %d recipients per wallet, got %d",
			transactions.NewQuotaExceededError(), maxRecipientsPerWallet, len(params.Recipients))
	}
	// The per-wallet ceiling is always positive (CreateCashHub refuses 0).
	ceiling := uint64(max(hubConfig.PerWalletMaxMloki, 0)) //nolint:gosec // non-negative
	var sum uint64
	// One slice per identity on a bill: the slice is looked up by (type, value),
	// so a repeat is refused whatever its ia_pubkey.
	seen := make(map[string]bool, len(params.Recipients))
	for i, r := range params.Recipients {
		switch r.IdentityType {
		case db.CashIdentityCash:
			// A cash note is a self-contained bearer object: redeeming it needs
			// the wallet's connection too, so a cash slice sharing a bill would
			// hand its holder a live channel into other recipients' wallet.
			if len(params.Recipients) != 1 {
				return nil, fmt.Errorf("%w: recipient %d: a cash-mode recipient must be the only recipient in this wallet",
					constants.ErrInvalidParams, i)
			}
			if r.IAPubkey != "" {
				return nil, fmt.Errorf("%w: recipient %d: a cash entry must not carry ia_pubkey",
					constants.ErrInvalidParams, i)
			}
		case db.CashIdentityPubkey, db.CashIdentityConnectionKey:
			if err := validateIdentityForm(r.IdentityType, r.IdentityValue, r.IAPubkey); err != nil {
				return nil, fmt.Errorf("recipient %d: %w", i, err)
			}
		default:
			return nil, fmt.Errorf("%w: recipient %d: identity_type must be %q, %q, or %q", constants.ErrInvalidParams,
				i, db.CashIdentityPubkey, db.CashIdentityConnectionKey, db.CashIdentityCash)
		}
		key := r.IdentityType + ":" + r.IdentityValue
		if seen[key] {
			return nil, fmt.Errorf("%w: recipient %d: duplicate identity in this request", constants.ErrInvalidParams, i)
		}
		seen[key] = true

		if r.AmountMloki == 0 {
			return nil, fmt.Errorf("%w: recipient %d: amount_millis must be positive", constants.ErrInvalidParams, i)
		}
		// Each amount is bounded before it is added, so the sum below works on
		// bounded terms (NIP-CASH §Security Considerations).
		if r.AmountMloki > ceiling {
			return nil, fmt.Errorf("%w: recipient %d: amount %d exceeds per_wallet_max_mloki %d",
				transactions.NewQuotaExceededError(), i, r.AmountMloki, ceiling)
		}
		// The sum must stay an int64: the balance comparison and the slice rows
		// use int64.
		if r.AmountMloki > uint64(math.MaxInt64)-sum {
			return nil, fmt.Errorf("%w: recipient %d: combined recipient amounts overflow", constants.ErrInvalidParams, i)
		}
		sum += r.AmountMloki
	}
	if sum > ceiling {
		return nil, fmt.Errorf("%w: total amount %d exceeds per_wallet_max_mloki %d",
			transactions.NewQuotaExceededError(), sum, ceiling)
	}

	// Step 4: every Identity Authority trusted now, every commitment well formed,
	// no refused field.
	for i, r := range params.Recipients {
		switch r.IdentityType {
		case db.CashIdentityConnectionKey:
			if err := checkIATrusted(deps, r.IAPubkey); err != nil {
				return nil, fmt.Errorf("recipient %d: %w", i, err)
			}
		case db.CashIdentityCash:
			// The caller's commitment of a secret it generated; the Hub never
			// sees the secret (NIP-CASH §Minting Cash).
			if !nipcash.ValidCommitment(r.IdentityValue) {
				return nil, fmt.Errorf("%w: recipient %d: a cash entry's identity_value must be a 64-character lowercase hex commitment",
					constants.ErrInvalidParams, i)
			}
		}
	}
	if params.RefusedField != "" {
		return nil, fmt.Errorf("%w: %s is not a mint_cash parameter", constants.ErrInvalidParams, params.RefusedField)
	}

	// Step 5: expiry, one shared value for the whole wallet.
	//
	// A Hub ceiling of 0 means "never": an omitted/zero expiry then produces a
	// bill that never expires, and a caller's own finite expiry is honored up to
	// the representational bound. A positive ceiling caps an explicit expiry
	// (a longer one is RESTRICTED) and is the default for an omitted one.
	if params.ExpirySecs < 0 || params.ExpirySecs > constants.MAX_EXPIRY_SECS {
		return nil, fmt.Errorf("%w: expiry %d is outside 0 to %d", constants.ErrInvalidParams, params.ExpirySecs, constants.MAX_EXPIRY_SECS)
	}
	var expiresAt *time.Time
	if hubConfig.MaxExpSecs == 0 {
		if params.ExpirySecs > 0 {
			t := time.Now().Add(time.Duration(params.ExpirySecs) * time.Second)
			expiresAt = &t
		}
	} else {
		expirySecs := params.ExpirySecs
		if expirySecs == 0 {
			expirySecs = hubConfig.MaxExpSecs
		} else if expirySecs > hubConfig.MaxExpSecs {
			return nil, fmt.Errorf("%w: expiry %d exceeds max_exp_secs %d", ErrRestricted, params.ExpirySecs, hubConfig.MaxExpSecs)
		}
		t := time.Now().Add(time.Duration(expirySecs) * time.Second)
		expiresAt = &t
	}

	// Step 6: the Hub's balance. The commit's own transfer re-checks it under the
	// lock.
	balance := queries.GetIsolatedBalance(deps.DB, params.HubApp.ID)
	if int64(sum) > balance { //nolint:gosec // sum is bounded to <= MaxInt64 above
		return nil, fmt.Errorf("%w: insufficient balance in Cash Hub", transactions.NewInsufficientBalanceError())
	}

	return &Resolved{
		HubApp:             params.HubApp,
		Recipients:         append([]RecipientInput(nil), params.Recipients...),
		ExpiresAt:          expiresAt,
		MinTransferMloki:   hubConfig.MinTransferMloki,
		RedeemFeePpm:       hubConfig.RedeemFeePpm,
		RedeemFeeBaseMloki: hubConfig.RedeemFeeBaseMloki,
	}, nil
}

// validateIdentityForm checks a pubkey or connection_key identity's shape:
// 64-hex identity_value, and for connection_key a 64-hex ia_pubkey.
func validateIdentityForm(identityType, identityValue, iaPubkey string) error {
	if decoded, decErr := hex.DecodeString(identityValue); decErr != nil || len(decoded) != 32 {
		return fmt.Errorf("%w: identity_value must be a 64-character lowercase hex string", constants.ErrInvalidParams)
	}
	switch identityType {
	case db.CashIdentityPubkey:
		return nil
	case db.CashIdentityConnectionKey:
		if iaPubkey == "" {
			return fmt.Errorf("%w: ia_pubkey is required when identity_type is connection_key", constants.ErrInvalidParams)
		}
		if decoded, decErr := hex.DecodeString(iaPubkey); decErr != nil || len(decoded) != 32 {
			return fmt.Errorf("%w: ia_pubkey must be a valid 32-byte hex nostr pubkey", constants.ErrInvalidParams)
		}
		return nil
	default:
		return fmt.Errorf("%w: identity_type must be %q or %q", constants.ErrInvalidParams,
			db.CashIdentityPubkey, db.CashIdentityConnectionKey)
	}
}

// checkIATrusted refuses an Identity Authority not on the allowlist right now
// with ErrRestricted.
func checkIATrusted(deps Deps, iaPubkey string) error {
	if deps.IAChecker == nil {
		return fmt.Errorf("%w: no Identity Authority trust checker configured", ErrRestricted)
	}
	trusted, trustErr := deps.IAChecker.IsTrusted(iaPubkey)
	if trustErr != nil {
		return fmt.Errorf("failed to check Identity Authority trust: %w", trustErr)
	}
	if !trusted {
		return fmt.Errorf("%w: ia_pubkey is not a trusted Identity Authority", ErrRestricted)
	}
	return nil
}

// ValidateIdentityShape checks identity_type/identity_value/ia_pubkey for a
// pubkey or connection_key identity — hex shape, and, for connection_key,
// that ia_pubkey is present, hex-valid, and a currently-trusted Identity
// Authority. Shared by Resolve's per-recipient validation and cash_transfer's
// new_identity validation — the only two places this codebase accepts a
// caller-supplied (identity_type, identity_value, ia_pubkey) triple that
// isn't a cash commitment. Does not accept db.CashIdentityCash.
func ValidateIdentityShape(deps Deps, identityType, identityValue, iaPubkey string) error {
	if err := validateIdentityForm(identityType, identityValue, iaPubkey); err != nil {
		return err
	}
	if identityType == db.CashIdentityConnectionKey {
		return checkIATrusted(deps, iaPubkey)
	}
	return nil
}

// cashWalletScopes are the ONLY scopes ever granted to a cash_wallet child.
// Deliberately narrow: a cash_wallet's connection may be widely shared among
// its recipients, so its method surface is an explicit allowlist rather than
// a normal wallet's scope set. No pay_invoice/lookup_invoice (this app never
// makes or looks up its own invoices) and no list_transactions (which would
// leak every OTHER recipient's payout history — amount, timestamp, preimage —
// to anyone holding the shared connection).
//
// No get_balance either, for the SAME reason get_budget is carved out of the
// always-granted list for AppKindCashWallet (see nip47/event_handler.go): a
// bill's balance is the total funded across EVERY recipient, and get_balance
// handed it to anyone holding the shared connection with no proof required.
// get_budget was carved out on exactly that reasoning and get_balance was left
// in, which was inconsistent. A recipient's own entitlement is what they are
// owed, and cash_status reports it — scoped to them, over the private
// transport, and only once they have proven possession.
//
// Dropping it also removes the last reason to dial a bill on kind 23194 other
// than get_info. That matters because a 23194 request names its target in a
// clear-text "p" tag, so every such call tells a relay watcher that this
// specific bill is live and being used — exactly the correlation the private
// transport exists to remove.
//
// get_info stays, and is now load-bearing rather than incidental: its
// private_methods field is how a bill's holder discovers which methods are
// served over the private transport (NIP-CASH §Scope Surface). It reveals a
// method set, not money.
var cashWalletScopes = []string{
	constants.CASH_REDEEM_SCOPE,
	constants.CASH_TRANSFER_SCOPE,
	constants.CASH_CONSOLIDATE_SCOPE,
}

// Commit creates one spend-only cash_wallet child of resolved.HubApp serving every
// resolved recipient and funds it from the Hub, as one transaction (see commit):
// the bill, its slices, its signed token, the funding rows and, when record is
// set, the call's record either all commit or none do. record is written last,
// under its deadline.
func Commit(ctx context.Context, deps Deps, resolved *Resolved, record *db.CashRequestRecord) (*Result, error) {
	var sum uint64
	for _, r := range resolved.Recipients {
		sum += r.AmountMloki
	}

	claimRows := make([]db.CashWalletClaim, len(resolved.Recipients))
	for i, r := range resolved.Recipients {
		claimRows[i] = db.CashWalletClaim{
			IdentityType:       r.IdentityType,
			IdentityValue:      r.IdentityValue,
			IAPubkey:           r.IAPubkey,
			AmountMloki:        int64(r.AmountMloki), //nolint:gosec // bounded to <= MaxInt64 by Resolve
			MinTransferMloki:   resolved.MinTransferMloki,
			RedeemFeePpm:       resolved.RedeemFeePpm,
			RedeemFeeBaseMloki: resolved.RedeemFeeBaseMloki,
		}
	}

	// A cash-mode recipient is always the bill's only one (Resolve), so the first
	// recipient's mode speaks for the bill. The provenance attests the bill's total.
	out, err := commit(ctx, deps, commitPlan{
		op:       "mint",
		hub:      resolved.HubApp,
		lockApps: []uint{resolved.HubApp.ID},
		bills: []newBill{{
			role:             db.CashRecordBillMinted,
			nameSeed:         resolved.Recipients[0].IdentityValue,
			amount:           sum,
			expiresAt:        resolved.ExpiresAt,
			claims:           claimRows,
			identityRequired: resolved.Recipients[0].IdentityType != db.CashIdentityCash,
		}},
		transfers: []internalTransfer{{fromAppID: resolved.HubApp.ID, toBill: 0, amount: sum}},
		record:    record,
	})
	if err != nil {
		return nil, err
	}
	bill := out[0]

	recipientResults := make([]RecipientResult, len(resolved.Recipients))
	for i, r := range resolved.Recipients {
		recipientResults[i] = RecipientResult{
			IdentityType:  r.IdentityType,
			IdentityValue: r.IdentityValue,
			IAPubkey:      r.IAPubkey,
			AmountMloki:   r.AmountMloki,
		}
	}

	logger.Logger.Info().
		Uint("cash_wallet_id", bill.App.ID).
		Uint("parent_app_id", resolved.HubApp.ID).
		Int("recipient_count", len(resolved.Recipients)).
		Uint64("total_mloki", sum).
		Msg("Shared Cash wallet created and funded")

	return &Result{
		WalletApp:  bill.App,
		CashToken:  bill.Token,
		ExpiresAt:  resolved.ExpiresAt,
		Recipients: recipientResults,
	}, nil
}

// MintRecord is a mint's record (NIP-CASH §The Record): the Hub's connection is
// its target and its authorizer, and result_meta holds the reply's non-secret
// fields other than the bill itself, which the record's bill row carries.
func MintRecord(requestID string, hub *db.App, deadline int64, resolved *Resolved) (*db.CashRequestRecord, error) {
	target := hub.AppPubkey
	if hub.WalletPubkey != nil && *hub.WalletPubkey != "" {
		target = *hub.WalletPubkey
	}
	meta := struct {
		ExpiresAt  *int64                    `json:"expires_at,omitempty"`
		Recipients []nipcash.RecipientResult `json:"recipients"`
	}{Recipients: make([]nipcash.RecipientResult, len(resolved.Recipients))}
	for i, r := range resolved.Recipients {
		meta.Recipients[i] = nipcash.RecipientResult{
			IdentityType:  r.IdentityType,
			IdentityValue: r.IdentityValue,
			IAPubkey:      r.IAPubkey,
			AmountMillis:  r.AmountMloki,
		}
	}
	if resolved.ExpiresAt != nil {
		at := resolved.ExpiresAt.Unix()
		meta.ExpiresAt = &at
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	return &db.CashRequestRecord{
		RequestID:          requestID,
		Method:             constants.NIP47MethodMintCash,
		HubAppID:           hub.ID,
		TargetWalletPubkey: target,
		TargetConnPubkey:   hub.AppPubkey,
		AuthorizerKind:     "client",
		Authorizer:         hub.AppPubkey,
		Deadline:           deadline,
		ResultMeta:         string(raw),
	}, nil
}

// SplitResult is one bill a split or consolidate created and the token it was
// issued with. The caller delivers the token (nested-encrypted where the method
// says so); this package knows nothing of NIP-47 encryption.
type SplitResult struct {
	WalletApp *db.App
	CashToken string
	// Zap is set for the bill of a zap transfer: its signed issued_at and
	// expires_at (0 for never), which the reply carries exactly as signed.
	Zap *ZapTimes
}

// ZapTimes are a zap bill's signed issued_at and expires_at, in unix seconds.
type ZapTimes struct {
	IssuedAt, ExpiresAt uint64
}

// Create resolves every recipient, creates a spend-only cash_wallet child of
// params.HubApp serving all of them, and funds it via one internal transfer.
// It is exactly Resolve followed by Commit, for callers (e.g. the admin HTTP
// API) that don't need to gate anything — like a rate limit — between
// validation and the actual mutating creation.
func Create(ctx context.Context, deps Deps, params Params) (*Result, error) {
	resolved, err := Resolve(ctx, deps, params)
	if err != nil {
		return nil, err
	}
	return Commit(ctx, deps, resolved, nil)
}
