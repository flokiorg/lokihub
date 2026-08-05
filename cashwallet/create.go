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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/transactions"
	"github.com/nbd-wtf/go-nostr"
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
// hubAppID alone is safe. Mirrors create_circle_wallet_controller.go's
// activeCircleInvoices, which only needs to be controller-local because
// circle wallet creation has exactly one call site — Cash wallet creation has
// two, so the guard has to live where both can reach it.
var activeHubCommits sync.Map // map[uint]struct{}

// LockHub attempts to acquire the in-process creation slot for hubAppID. ok
// is false if another mint_cash call for this hub is already in
// flight; the caller should reject the request rather than block, mirroring
// activeCircleInvoices's behavior. Callers should defer release() immediately
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
}

// RecipientInput describes one recipient's requested slice of a shared Cash
// wallet. IAPubkey is only meaningful when IdentityType is
// db.CashIdentityConnectionKey. For db.CashIdentityBearer, the caller
// MUST leave IdentityValue and IAPubkey empty — Resolve generates the slice's
// secret itself and fills in IdentityValue (as the secret's hash) and
// BearerSecret (the plaintext, populated by Resolve for Commit/the caller to
// return exactly once — never read back out of the caller's own input).
type RecipientInput struct {
	IdentityType  string // db.CashIdentityPubkey | db.CashIdentityConnectionKey | db.CashIdentityBearer
	IdentityValue string
	IAPubkey      string
	AmountMloki   uint64
	BearerSecret  string
}

// Params describes the shared wallet to create. MinTransferMloki is
// deliberately not a field here: it's a Cash-Hub-level setting
// (db.CashHubConfig.MinTransferMloki) Resolve reads directly, not a
// per-call value a caller supplies — see Resolved.MinTransferMloki.
type Params struct {
	HubApp     *db.App
	Recipients []RecipientInput
	ExpirySecs int
}

// RecipientResult echoes back one recipient's resolved/committed slice.
// BearerSecret is populated only for a db.CashIdentityBearer recipient,
// and only this once — it is never retrievable again after this response
// (NIP-JW §Bearer Slices).
type RecipientResult struct {
	IdentityType  string
	IdentityValue string
	AmountMloki   uint64
	BearerSecret  string
}

// Result carries everything a caller needs to build its own protocol-specific
// response. PairingURI is always plaintext and always populated — unlike the
// old per-recipient design, there is no encrypted-reveal step: the wallet's
// connection is meant to be distributed to the whole recipient group by
// whoever created it.
type Result struct {
	WalletApp  *db.App
	PairingURI string
	CashToken  string
	ExpiresAt  time.Time
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
	// Recipients: amounts already validated. IdentityType/Value are unchanged
	// from the caller's input for pubkey/connection_key recipients; for a
	// bearer recipient, IdentityValue and BearerSecret were just generated
	// by Resolve (the caller supplied neither).
	Recipients []RecipientInput
	ExpiresAt  time.Time
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
}

// maxRecipientsPerWallet mirrors apps.maxRecipientsPerWallet — duplicated as
// a small local constant since apps doesn't export its own (kept private to
// avoid the two packages needing to agree on an exported name for a single
// shared limit that's re-validated at insert time anyway).
const maxRecipientsPerWallet = 100

// Resolve performs every read-only validation needed to create a shared Cash
// wallet, without creating or funding anything. It's the counterpart to
// Commit — see Create, which is just Resolve followed by Commit for callers
// that don't need to gate anything in between.
func Resolve(ctx context.Context, deps Deps, params Params) (*Resolved, error) {
	if params.HubApp.Kind != db.AppKindCashHub {
		return nil, fmt.Errorf("%w: mint_cash requires a cash_hub app", constants.ErrInvalidParams)
	}
	if len(params.Recipients) == 0 {
		return nil, fmt.Errorf("%w: recipients list is empty", constants.ErrInvalidParams)
	}
	if len(params.Recipients) > maxRecipientsPerWallet {
		return nil, fmt.Errorf("%w: at most %d recipients per wallet, got %d",
			constants.ErrInvalidParams, maxRecipientsPerWallet, len(params.Recipients))
	}
	// A bearer note is meant to be a self-contained, freely-handed-off
	// object, like cash — whoever it's given to needs nothing else to
	// redeem it. Mixing a bearer slice into a wallet that also serves other,
	// identity-bound recipients would break that: redeeming the bearer
	// slice requires the wallet's connection too, so handing the note to
	// someone would also hand them a live channel into a multi-recipient
	// wallet that isn't theirs. A bearer wallet is always exactly one slice.
	for i, r := range params.Recipients {
		if r.IdentityType == db.CashIdentityBearer && len(params.Recipients) != 1 {
			return nil, fmt.Errorf("%w: recipient %d: a bearer recipient must be the only recipient in this wallet",
				constants.ErrInvalidParams, i)
		}
	}

	hubConfig, err := deps.AppsService.GetCashHubConfig(params.HubApp.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load Cash Hub config: %w", err)
	}

	// Expiration: one shared value for the whole wallet — cap the caller's
	// requested duration, or default to the hub's own max when omitted (an
	// omitted/zero expiry used to produce an already-expired wallet — this
	// closes that gap).
	expirySecs := params.ExpirySecs
	if expirySecs <= 0 {
		expirySecs = hubConfig.MaxExpSecs
	} else if hubConfig.MaxExpSecs > 0 && expirySecs > hubConfig.MaxExpSecs {
		return nil, fmt.Errorf("%w: expiry %d exceeds max_exp_secs %d", constants.ErrInvalidParams, params.ExpirySecs, hubConfig.MaxExpSecs)
	}
	expiresAt := time.Now().Add(time.Duration(expirySecs) * time.Second)

	var sum uint64
	seen := make(map[string]bool, len(params.Recipients))
	resolvedRecipients := make([]RecipientInput, len(params.Recipients))
	for i, r := range params.Recipients {
		bearerMode := r.IdentityType == db.CashIdentityBearer
		connKeyMode := r.IdentityType == db.CashIdentityConnectionKey
		if !bearerMode && !connKeyMode && r.IdentityType != db.CashIdentityPubkey {
			return nil, fmt.Errorf("%w: recipient %d: identity_type must be %q, %q, or %q", constants.ErrInvalidParams,
				i, db.CashIdentityPubkey, db.CashIdentityConnectionKey, db.CashIdentityBearer)
		}

		if bearerMode {
			// The caller supplies no identity for a bearer recipient — the
			// Hub is the only party that can vouch for its secret's entropy,
			// so it generates (and hashes) that secret itself, here.
			if r.IdentityValue != "" || r.IAPubkey != "" {
				return nil, fmt.Errorf("%w: recipient %d: bearer mode must not carry identity_value or ia_pubkey",
					constants.ErrInvalidParams, i)
			}
			secretHex, secretHash, genErr := GenerateBearerSecret()
			if genErr != nil {
				return nil, fmt.Errorf("failed to generate bearer secret: %w", genErr)
			}
			r.IdentityValue = secretHash
			r.BearerSecret = secretHex
			// No dedupe check: a fresh, independently-random secret can't
			// collide with anything already `seen`, and a bearer recipient
			// is already guaranteed to be the only one in this request by
			// the mixing check above.
		} else {
			if err := ValidateIdentityShape(deps, r.IdentityType, r.IdentityValue, r.IAPubkey); err != nil {
				return nil, fmt.Errorf("recipient %d: %w", i, err)
			}
			dedupeKey := r.IdentityType + ":" + r.IdentityValue
			if seen[dedupeKey] {
				return nil, fmt.Errorf("%w: recipient %d: duplicate identity in this request", constants.ErrInvalidParams, i)
			}
			seen[dedupeKey] = true
		}

		if r.AmountMloki == 0 {
			return nil, fmt.Errorf("%w: recipient %d: amount_mloki must be positive", constants.ErrInvalidParams, i)
		}
		// Reject a single value large enough to overflow int64 on casts used
		// downstream (balance/quota comparisons), mirroring
		// create_circle_wallet_controller.go's identical guard on its own
		// (single, unsummed) max_amount.
		if r.AmountMloki > math.MaxInt64 {
			return nil, fmt.Errorf("%w: recipient %d: amount_mloki %d is too large", constants.ErrInvalidParams, i, r.AmountMloki)
		}
		// Reject a sum that would exceed MaxInt64 — with N recipients each
		// individually under MaxInt64, the running total could still overflow
		// before the PerWalletMaxMloki check below ever sees it, silently
		// wrapping to a small (or negative-when-cast) value that passes both
		// that cap and the int64(sum) > balance check further down, while
		// leaving individual recipients' stored entitlements at their
		// original, un-wrapped (and uncollectable) amounts. Bounded by
		// MaxInt64 rather than MaxUint64 specifically because that's what
		// the balance comparison below actually casts sum into. Caught with
		// a pre-add overflow check rather than trusting the cap comparison
		// after the fact.
		if r.AmountMloki > uint64(math.MaxInt64)-sum {
			return nil, fmt.Errorf("%w: recipient %d: combined recipient amounts overflow", constants.ErrInvalidParams, i)
		}

		sum += r.AmountMloki
		resolvedRecipients[i] = r
	}

	// PerWalletMaxMloki now caps the wallet's TOTAL (sum across every
	// recipient) — a clean generalization of "per wallet" now that a wallet
	// serves N recipients rather than exactly one.
	if hubConfig.PerWalletMaxMloki > 0 && sum > uint64(hubConfig.PerWalletMaxMloki) {
		return nil, fmt.Errorf("%w: total amount %d exceeds per_wallet_max_mloki %d",
			transactions.NewQuotaExceededError(), sum, hubConfig.PerWalletMaxMloki)
	}

	// Pre-flight balance check (the transfer itself is the authoritative check).
	balance := queries.GetIsolatedBalance(deps.DB, params.HubApp.ID)
	if int64(sum) > balance { //nolint:gosec // sum is bounded to <= MaxInt64 by the per-recipient/running-total guards above
		return nil, fmt.Errorf("%w: insufficient balance in Cash Hub", transactions.NewInsufficientBalanceError())
	}

	return &Resolved{
		HubApp:           params.HubApp,
		Recipients:       resolvedRecipients,
		ExpiresAt:        expiresAt,
		MinTransferMloki: hubConfig.MinTransferMloki,
		RedeemFeePpm:     hubConfig.RedeemFeePpm,
	}, nil
}

// ValidateIdentityShape checks identity_type/identity_value/ia_pubkey for a
// pubkey or connection_key identity — hex shape, and, for connection_key,
// that ia_pubkey is present, hex-valid, and a currently-trusted Identity
// Authority. Shared by Resolve's per-recipient validation and cash_transfer's
// new_identity validation — the only two places this codebase accepts a
// caller-supplied (identity_type, identity_value, ia_pubkey) triple that
// isn't a bearer secret. Does not accept db.CashIdentityBearer: a bearer
// target has no caller-supplied shape to validate — see GenerateBearerSecret.
func ValidateIdentityShape(deps Deps, identityType, identityValue, iaPubkey string) error {
	switch identityType {
	case db.CashIdentityPubkey:
		if decoded, decErr := hex.DecodeString(identityValue); decErr != nil || len(decoded) != 32 {
			return fmt.Errorf("%w: identity_value must be a 64-character lowercase hex string", constants.ErrInvalidParams)
		}
		return nil
	case db.CashIdentityConnectionKey:
		if decoded, decErr := hex.DecodeString(identityValue); decErr != nil || len(decoded) != 32 {
			return fmt.Errorf("%w: identity_value must be a 64-character lowercase hex string", constants.ErrInvalidParams)
		}
		if iaPubkey == "" {
			return fmt.Errorf("%w: ia_pubkey is required when identity_type is connection_key", constants.ErrInvalidParams)
		}
		if decoded, decErr := hex.DecodeString(iaPubkey); decErr != nil || len(decoded) != 32 {
			return fmt.Errorf("%w: ia_pubkey must be a valid 32-byte hex nostr pubkey", constants.ErrInvalidParams)
		}
		if deps.IAChecker == nil {
			return fmt.Errorf("%w: no Identity Authority trust checker configured", constants.ErrInvalidParams)
		}
		trusted, trustErr := deps.IAChecker.IsTrusted(iaPubkey)
		if trustErr != nil {
			return fmt.Errorf("failed to check Identity Authority trust: %w", trustErr)
		}
		if !trusted {
			return fmt.Errorf("%w: ia_pubkey is not a trusted Identity Authority", constants.ErrInvalidParams)
		}
		return nil
	default:
		return fmt.Errorf("%w: identity_type must be %q or %q", constants.ErrInvalidParams,
			db.CashIdentityPubkey, db.CashIdentityConnectionKey)
	}
}

// bearerSecretLen is 32 bytes — same size as every other Nostr key/secret in
// this codebase, and comfortably enough entropy that guessing a bearer
// secret is infeasible (NIP-JW §Bearer Slices).
const bearerSecretLen = 32

// GenerateBearerSecret returns a fresh, high-entropy bearer secret (hex) and
// the hex-encoded SHA-256 hash that gets persisted in its place — the raw
// secret itself is never written to storage, only ever handed back once, in
// the response that generated it. Shared by Resolve (mint_cash) and
// cash_transfer, whenever either mints a new bearer slice.
func GenerateBearerSecret() (secretHex, secretHash string, err error) {
	var secret [bearerSecretLen]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", "", err
	}
	hash := sha256.Sum256(secret[:])
	return hex.EncodeToString(secret[:]), hex.EncodeToString(hash[:]), nil
}

// cashWalletScopes are the ONLY scopes ever granted to a cash_wallet child.
// Deliberately narrow: a cash_wallet's connection may be widely shared among
// its recipients, so its method surface is an explicit allowlist rather than
// a normal wallet's scope set. No pay_invoice/lookup_invoice (this app never
// makes or looks up its own invoices) and no list_transactions (which would
// leak every OTHER recipient's payout history — amount, timestamp, preimage —
// to anyone holding the shared connection). get_info stays reachable via the
// system-wide "always granted" list; get_budget is explicitly carved out of
// that same list for AppKindCashWallet (see nip47/event_handler.go) since it
// would otherwise reveal the wallet's total funded amount across every
// recipient with no proof required.
var cashWalletScopes = []string{
	constants.CASH_REDEEM_SCOPE,
	constants.CASH_TRANSFER_SCOPE,
	constants.GET_BALANCE_SCOPE,
}

// Commit creates one spend-only cash_wallet child of resolved.HubApp serving
// every resolved recipient, and funds it via a single internal transfer sized
// to their combined total, using values already validated by Resolve. If
// funding fails, the child app (and its recipient rows) are rolled back
// (deleted); once funds have moved, nothing after that point deletes the
// wallet, since doing so would produce a ledger imbalance.
func Commit(ctx context.Context, deps Deps, resolved *Resolved) (*Result, error) {
	var sum uint64
	for _, r := range resolved.Recipients {
		sum += r.AmountMloki
	}

	newApp, _, err := deps.AppsService.CreateApp(
		apps.GenerateChildName(resolved.HubApp.Name, resolved.Recipients[0].IdentityValue),
		"", // generate a temporary random keypair; overridden immediately below
		sum/1000,
		constants.BUDGET_RENEWAL_NEVER,
		&resolved.ExpiresAt,
		cashWalletScopes,
		db.AppKindCashWallet,
		&resolved.HubApp.ID,
		db.ParentKindCash,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create Cash wallet app: %w", err)
	}

	// Everything from here until the transfer below is reversible bookkeeping:
	// if any of it fails, or the transfer itself fails, this defer undoes it
	// by deleting the just-created app (and its CashWalletClaim rows, via FK
	// cascade). The transfer is deliberately the last thing this function
	// does, specifically so that once fundsTransferred is true there is
	// nothing left that could fail and leave the wallet in an inconsistent or
	// invisible state.
	fundsTransferred := false
	defer func() {
		if fundsTransferred {
			return
		}
		_ = deps.AppsService.DeleteApp(newApp)
	}()

	// Derive the deterministic pairing private key from the app ID (BIP32 branch H+2).
	// This key never needs to be stored — it can be re-derived any time via
	// keys.GetCashPairingKey/api.GetCashWalletConnection.
	pairingSecretKey, err := deps.Keys.GetCashPairingKey(newApp.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to derive Cash pairing key: %w", err)
	}
	deterministicPubKey, err := nostr.GetPublicKey(pairingSecretKey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive Cash pairing pubkey: %w", err)
	}
	if err := deps.DB.Model(&db.App{}).Where("id = ?", newApp.ID).
		Update("app_pubkey", deterministicPubKey).Error; err != nil {
		return nil, fmt.Errorf("failed to register pairing key: %w", err)
	}
	newApp.AppPubkey = deterministicPubKey
	walletPubkey := *newApp.WalletPubkey

	claimRows := make([]db.CashWalletClaim, len(resolved.Recipients))
	for i, r := range resolved.Recipients {
		claimRows[i] = db.CashWalletClaim{
			IdentityType:     r.IdentityType,
			IdentityValue:    r.IdentityValue,
			IAPubkey:         r.IAPubkey,
			AmountMloki:      int64(r.AmountMloki), //nolint:gosec // resolved.Recipients' amounts are already bounded to <= MaxInt64 by Resolve, which Commit's only callers always invoke first
			MinTransferMloki: resolved.MinTransferMloki,
			RedeemFeePpm:     resolved.RedeemFeePpm,
		}
	}
	if err := deps.AppsService.CreateCashWalletClaims(newApp.ID, claimRows); err != nil {
		return nil, fmt.Errorf("failed to store recipient claims: %w", err)
	}

	// Transfer funds from Cash Hub to Cash wallet. This is the one genuinely
	// irreversible step in this function, which is why it happens last: by
	// this point every other side effect is already durably committed, so
	// nothing remains that could fail and leave the wallet stranded or
	// invisible.
	invoice, err := deps.TransactionsService.MakeInvoice(
		ctx, sum, "cash transfer", "", 0,
		nil, deps.LNClient, &newApp.ID, nil, nil, nil, nil, nil, nil,
		&transactions.InternalMakeInvoiceMeta{InternalTransfer: true},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer invoice for Cash wallet: %w", err)
	}

	_, err = deps.TransactionsService.SendPaymentSync(
		invoice.PaymentRequest, nil,
		map[string]interface{}{"internal_transfer": true},
		deps.LNClient, &resolved.HubApp.ID, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to fund Cash wallet via transfer: %w", err)
	}
	fundsTransferred = true

	recipientResults := make([]RecipientResult, len(resolved.Recipients))
	for i, r := range resolved.Recipients {
		result := RecipientResult{
			IdentityType: r.IdentityType,
			AmountMloki:  r.AmountMloki,
		}
		if r.IdentityType == db.CashIdentityBearer {
			// r.IdentityValue here is the secret's hash — an internal
			// storage detail, never meant for the wire response. Only the
			// plaintext secret is; the hash on its own is useless to a
			// recipient and would just be a stray, meaningless-looking field.
			result.BearerSecret = r.BearerSecret
		} else {
			result.IdentityValue = r.IdentityValue
		}
		recipientResults[i] = result
	}

	// walletPubkey and pairingSecretKey are both derived internally (never
	// user input), so lokicash.Encode can't fail on them in practice. Funds
	// have already moved (fundsTransferred, above) by this point, so even a
	// defensive failure here must not turn into an error return — that would
	// tell the caller wallet creation failed when it actually succeeded,
	// leaving a funded wallet the caller doesn't know exists. Degrade to an
	// empty token instead; PairingURI alone is still a fully functional
	// connection string.
	// Uniform across every recipient by construction (Resolve requires a
	// bearer recipient to be this request's only one), so the first
	// recipient's identity type speaks for the whole wallet.
	identityRequired := resolved.Recipients[0].IdentityType != db.CashIdentityBearer
	lokicashToken, err := lokicash.Encode(lokicash.Token{
		HRP:              lokicash.HRP,
		WalletPubkey:     walletPubkey,
		Secret:           pairingSecretKey,
		RelayURLs:        deps.RelayURLs,
		IdentityRequired: &identityRequired,
	})
	if err != nil {
		logger.Logger.Error().Err(err).Uint("cash_wallet_id", newApp.ID).
			Msg("Failed to encode lokicash token for already-funded Cash wallet")
	}

	logger.Logger.Info().
		Uint("cash_wallet_id", newApp.ID).
		Uint("parent_app_id", resolved.HubApp.ID).
		Int("recipient_count", len(resolved.Recipients)).
		Uint64("total_mloki", sum).
		Msg("Shared Cash wallet created and funded")

	return &Result{
		WalletApp:  newApp,
		PairingURI: buildNWCPairingURI(walletPubkey, deps.RelayURLs, pairingSecretKey),
		CashToken:  lokicashToken,
		ExpiresAt:  resolved.ExpiresAt,
		Recipients: recipientResults,
	}, nil
}

// SplitParams describes moving a full or partial amount out of an existing
// cash_wallet's slice into a brand-new, dedicated wallet — see NIP-CASH's
// "Splitting/Transferring a Slice" section. Unlike Commit (which funds its
// new child from HubApp), the funding source here is a cash_wallet, not the
// hub: HubApp only supplies naming/lineage (the new wallet is a child of the
// SAME hub the source wallet already belongs to), while SourceWalletApp is
// the actual isolated-balance app the transfer pays from.
type SplitParams struct {
	HubApp          *db.App
	SourceWalletApp *db.App
	// AmountMloki is the exact value moved into the new wallet — either the
	// source slice's full remaining amount or a partial piece of it, already
	// validated (including the MinTransferMloki floor on both the split
	// amount and whatever's left behind) by the caller
	// (AppsService.SplitCashSliceAmount) before this is called.
	AmountMloki uint64
	// NewIdentityType/NewIdentityValue/NewIAPubkey describe the new wallet's
	// sole recipient — any identity mode (pubkey, connection_key, or
	// bearer), not bearer-only: the unified transfer/split model spins off a
	// dedicated wallet for every target type once a wallet's recipient
	// history rules out a cheap in-place reassignment (see
	// cash_transfer_controller.go), not only when converting into bearer.
	// For bearer, NewIdentityValue is the caller-supplied sha256(secret) hex
	// commitment — never a secret this package mints, for the same reason
	// cash_transfer's in-place bearer path requires a caller-supplied
	// commitment (see cash_transfer_controller.go's bearer branch doc
	// comment: a server-minted secret would be readable by every co-recipient
	// of the connection this response travels over before the caller could
	// ever deliver it — Split exists specifically to give those slices
	// somewhere safe to go instead).
	NewIdentityType  string
	NewIdentityValue string
	NewIAPubkey      string
	// MinTransferMloki is inherited from the source slice, preserving
	// whatever split floor the wallet's creator originally configured rather
	// than resetting it on split — NIP-CASH's inheritance rule.
	MinTransferMloki int64
	// RedeemFeePpm is likewise inherited from the source slice, unchanged —
	// same inheritance rule as MinTransferMloki: a later change to the hub's
	// own config must never retroactively change the rate for an
	// already-issued lokicash.
	RedeemFeePpm int
	ExpiresAt    time.Time
}

// SplitResult carries what cash_transfer_controller.go needs to deliver the
// new wallet's connection to its caller — deliberately narrower than Result:
// a split-off wallet's connection is never broadcast in the clear the way a
// freshly mint_cash-issued one is (Result.PairingURI). The caller
// NIP-44 encrypts CashToken to the recipient's own pubkey before it ever
// reaches a response, keyed on WalletApp's own wallet keypair (mirroring
// create_circle_wallet_controller.go's identical encryptedURI/WalletPubkey
// pattern — see that controller's doc comment) — this package deliberately
// stays ignorant of NIP-47 encryption, same as it does everywhere else (see
// the package doc comment), so that step happens in the caller, not here.
type SplitResult struct {
	WalletApp *db.App
	CashToken string
}

// Split creates one spend-only, single-slice cash_wallet child of
// params.HubApp and funds it via a single internal transfer from
// params.SourceWalletApp's own isolated balance — structurally the same
// create-then-fund-last, rollback-via-DeleteApp shape as Commit (see that
// function's doc comment for the rationale), just with the parent and the
// funding source split into two different apps instead of one.
//
// The caller is responsible for having already exclusively claimed or
// decremented the source slice (AppsService.SplitCashSliceAmount) before
// calling this, and for rolling that back (UnclaimCashSlice for a full
// split, UndoCashSliceSplit for a partial one) if this returns an error —
// Split itself never touches the source wallet's CashWalletClaim rows, only
// its balance.
func Split(ctx context.Context, deps Deps, params SplitParams) (*SplitResult, error) {
	newApp, _, err := deps.AppsService.CreateApp(
		apps.GenerateChildName(params.HubApp.Name, params.NewIdentityValue),
		"", // generate a temporary random keypair; overridden immediately below
		params.AmountMloki/1000,
		constants.BUDGET_RENEWAL_NEVER,
		&params.ExpiresAt,
		cashWalletScopes,
		db.AppKindCashWallet,
		&params.HubApp.ID,
		db.ParentKindCash,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create split-off Cash wallet app: %w", err)
	}

	// Same reversible-until-funded shape as Commit: everything below is
	// undone by deleting newApp unless the transfer at the very end succeeds.
	fundsTransferred := false
	defer func() {
		if fundsTransferred {
			return
		}
		_ = deps.AppsService.DeleteApp(newApp)
	}()

	pairingSecretKey, err := deps.Keys.GetCashPairingKey(newApp.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to derive Cash pairing key: %w", err)
	}
	deterministicPubKey, err := nostr.GetPublicKey(pairingSecretKey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive Cash pairing pubkey: %w", err)
	}
	if err := deps.DB.Model(&db.App{}).Where("id = ?", newApp.ID).
		Update("app_pubkey", deterministicPubKey).Error; err != nil {
		return nil, fmt.Errorf("failed to register pairing key: %w", err)
	}
	newApp.AppPubkey = deterministicPubKey
	walletPubkey := *newApp.WalletPubkey

	if err := deps.AppsService.CreateCashWalletClaims(newApp.ID, []db.CashWalletClaim{{
		IdentityType:     params.NewIdentityType,
		IdentityValue:    params.NewIdentityValue,
		IAPubkey:         params.NewIAPubkey,
		AmountMloki:      int64(params.AmountMloki), //nolint:gosec // bounded to <= MaxInt64 by the source slice's own AmountMloki, already validated when that slice was created/resolved
		MinTransferMloki: params.MinTransferMloki,
		RedeemFeePpm:     params.RedeemFeePpm,
	}}); err != nil {
		return nil, fmt.Errorf("failed to store split-off recipient claim: %w", err)
	}

	// The one irreversible step, done last for the same reason Commit does:
	// once funds move, nothing after this can fail and leave the new wallet
	// stranded or invisible. Pays from SourceWalletApp (an isolated-balance
	// cash_wallet), not HubApp — validateCanPay's isolated-balance check and
	// enforceCashFullDrain both key off the PAYING app, and internal_transfer
	// metadata (stripped at every external NWC entry point) exempts this
	// privileged, Go-level-only transfer from both the full-drain guard and
	// the pay_invoice-scope requirement a cash_wallet's real NWC connection
	// deliberately never has.
	invoice, err := deps.TransactionsService.MakeInvoice(
		ctx, params.AmountMloki, "cash split", "", 0,
		nil, deps.LNClient, &newApp.ID, nil, nil, nil, nil, nil, nil,
		&transactions.InternalMakeInvoiceMeta{InternalTransfer: true},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer invoice for split-off Cash wallet: %w", err)
	}

	_, err = deps.TransactionsService.SendPaymentSync(
		invoice.PaymentRequest, nil,
		map[string]interface{}{"internal_transfer": true},
		deps.LNClient, &params.SourceWalletApp.ID, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to fund split-off Cash wallet via transfer: %w", err)
	}
	fundsTransferred = true

	// A split-off wallet is always a single slice, by construction (Split
	// never creates anything else) — no need to inspect the just-created
	// claim to know whether identity is required.
	identityRequired := params.NewIdentityType != db.CashIdentityBearer
	lokicashToken, err := lokicash.Encode(lokicash.Token{
		HRP:              lokicash.HRP,
		WalletPubkey:     walletPubkey,
		Secret:           pairingSecretKey,
		RelayURLs:        deps.RelayURLs,
		IdentityRequired: &identityRequired,
	})
	if err != nil {
		// Same reasoning as Commit: funds already moved (fundsTransferred,
		// above), so a defensive encode failure here must not become an
		// error return — that would tell the caller the split failed when it
		// actually succeeded, leaving a funded wallet with no way to deliver
		// its connection. Degrade to an empty token; the caller logs and the
		// wallet remains recoverable via the admin API.
		logger.Logger.Error().Err(err).Uint("cash_wallet_id", newApp.ID).
			Msg("Failed to encode lokicash token for already-funded split-off Cash wallet")
	}

	logger.Logger.Info().
		Uint("cash_wallet_id", newApp.ID).
		Uint("parent_app_id", params.HubApp.ID).
		Uint("source_wallet_id", params.SourceWalletApp.ID).
		Uint64("amount_mloki", params.AmountMloki).
		Msg("Slice split off into a new dedicated Cash wallet")

	return &SplitResult{
		WalletApp: newApp,
		CashToken: lokicashToken,
	}, nil
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
	return Commit(ctx, deps, resolved)
}

// buildNWCPairingURI assembles the nostr+walletconnect pairing URI. Duplicated
// (rather than imported) from nip47/controllers/pairing.go: it's an 8-line
// string builder, and duplicating it keeps this package free of a dependency
// on nip47/controllers, which would otherwise be the only import edge from a
// core business-logic package into a protocol package.
func buildNWCPairingURI(walletPubkey string, relayUrls []string, secret string) string {
	var b strings.Builder
	b.WriteString("nostr+walletconnect://")
	b.WriteString(walletPubkey)
	b.WriteString("?relay=")
	b.WriteString(strings.Join(relayUrls, "&relay="))
	b.WriteString("&secret=")
	b.WriteString(secret)
	return b.String()
}
