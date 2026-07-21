// Package jitwallet holds the protocol-agnostic core of "create a shared JIT
// wallet under a jit_hub": validate every recipient, spin up one spend-only
// child app serving all of them, and fund it via a single internal transfer
// from the hub. Both the NIP-47 create_jit_wallet controller and the admin
// HTTP API call into this package, so the funding/rollback logic lives in
// exactly one place.
//
// A JIT wallet's connection is meant to be shared/handed out freely among its
// recipients — knowing the connection alone never lets you spend, because
// claim_funds (see nip47/controllers/claim_funds_controller.go) requires each
// recipient to separately prove which identity they are before paying out
// their own slice. This package only concerns itself with creating that
// shared wallet and its recipient slices; the proof-gated payout lives in the
// controller.
//
// This package deliberately knows nothing about NIP-47 (rate limiting) or
// HTTP — those are protocol concerns that stay in their callers.
package jitwallet

import (
	"context"
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
	"github.com/flokiorg/lokihub/transactions"
	"github.com/nbd-wtf/go-nostr"
	"gorm.io/gorm"
)

// activeHubCommits serializes concurrent create_jit_wallet attempts against
// the same jit_hub — across BOTH the NWC path
// (nip47/controllers/create_jit_wallet_controller.go) and the admin HTTP path
// (api.CreateJITWallet) — so Resolve's balance pre-check and Commit's actual
// fund transfer can't race a second, concurrent creation past a stale
// balance read. Lives here (package-level, not on either caller) because
// both callers must share the same lock/key space; app IDs are globally
// unique across every app kind in the single `apps` table, so keying on
// hubAppID alone is safe. Mirrors create_circle_wallet_controller.go's
// activeCircleInvoices, which only needs to be controller-local because
// circle wallet creation has exactly one call site — JIT wallet creation has
// two, so the guard has to live where both can reach it.
var activeHubCommits sync.Map // map[uint]struct{}

// LockHub attempts to acquire the in-process creation slot for hubAppID. ok
// is false if another create_jit_wallet call for this hub is already in
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

// Deps are the services jitwallet.Create needs. Callers construct this from
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
	// db.JITAllocIdentityConnectionKey, so callers that only ever create
	// pubkey-mode wallets may leave it nil.
	IAChecker IATrustChecker
}

// RecipientInput describes one recipient's requested slice of a shared JIT
// wallet. IAPubkey is only meaningful when IdentityType is
// db.JITAllocIdentityConnectionKey.
type RecipientInput struct {
	IdentityType  string // db.JITAllocIdentityPubkey | db.JITAllocIdentityConnectionKey
	IdentityValue string
	IAPubkey      string
	AmountMloki   uint64
}

// Params describes the shared wallet to create.
type Params struct {
	HubApp     *db.App
	Recipients []RecipientInput
	ExpirySecs int
}

// RecipientResult echoes back one recipient's resolved/committed slice.
type RecipientResult struct {
	IdentityType  string
	IdentityValue string
	AmountMloki   uint64
}

// Result carries everything a caller needs to build its own protocol-specific
// response. PairingURI is always plaintext and always populated — unlike the
// old per-recipient design, there is no encrypted-reveal step: the wallet's
// connection is meant to be distributed to the whole recipient group by
// whoever created it.
type Result struct {
	WalletApp  *db.App
	PairingURI string
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
	HubApp     *db.App
	Recipients []RecipientInput // amounts already validated, IdentityType/Value unchanged
	ExpiresAt  time.Time
}

// maxRecipientsPerWallet mirrors apps.maxRecipientsPerWallet — duplicated as
// a small local constant since apps doesn't export its own (kept private to
// avoid the two packages needing to agree on an exported name for a single
// shared limit that's re-validated at insert time anyway).
const maxRecipientsPerWallet = 100

// Resolve performs every read-only validation needed to create a shared JIT
// wallet, without creating or funding anything. It's the counterpart to
// Commit — see Create, which is just Resolve followed by Commit for callers
// that don't need to gate anything in between.
func Resolve(ctx context.Context, deps Deps, params Params) (*Resolved, error) {
	if params.HubApp.Kind != db.AppKindJITHub {
		return nil, fmt.Errorf("%w: create_jit_wallet requires a jit_hub app", constants.ErrInvalidParams)
	}
	if len(params.Recipients) == 0 {
		return nil, fmt.Errorf("%w: recipients list is empty", constants.ErrInvalidParams)
	}
	if len(params.Recipients) > maxRecipientsPerWallet {
		return nil, fmt.Errorf("%w: at most %d recipients per wallet, got %d",
			constants.ErrInvalidParams, maxRecipientsPerWallet, len(params.Recipients))
	}

	hubConfig, err := deps.AppsService.GetJITHubConfig(params.HubApp.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load JIT Hub config: %w", err)
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
	for i, r := range params.Recipients {
		connKeyMode := r.IdentityType == db.JITAllocIdentityConnectionKey
		if !connKeyMode && r.IdentityType != db.JITAllocIdentityPubkey {
			return nil, fmt.Errorf("%w: recipient %d: identity_type must be %q or %q", constants.ErrInvalidParams,
				i, db.JITAllocIdentityPubkey, db.JITAllocIdentityConnectionKey)
		}
		if decoded, decErr := hex.DecodeString(r.IdentityValue); decErr != nil || len(decoded) != 32 {
			return nil, fmt.Errorf("%w: recipient %d: identity_value must be a 64-character lowercase hex string",
				constants.ErrInvalidParams, i)
		}
		dedupeKey := r.IdentityType + ":" + r.IdentityValue
		if seen[dedupeKey] {
			return nil, fmt.Errorf("%w: recipient %d: duplicate identity in this request", constants.ErrInvalidParams, i)
		}
		seen[dedupeKey] = true

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
		// Reject a sum that would wrap around uint64 — with N recipients each
		// individually under MaxInt64, the running total could still overflow
		// uint64 before the PerWalletMaxMloki check below ever sees it, silently
		// wrapping to a small value that passes both that cap and the hub
		// balance check while leaving individual recipients' stored
		// entitlements at their original, un-wrapped (and uncollectable)
		// amounts. Caught with a pre-add overflow check rather than trusting
		// the cap comparison after the fact.
		if r.AmountMloki > math.MaxUint64-sum {
			return nil, fmt.Errorf("%w: recipient %d: combined recipient amounts overflow", constants.ErrInvalidParams, i)
		}

		if connKeyMode {
			if r.IAPubkey == "" {
				return nil, fmt.Errorf("%w: recipient %d: ia_pubkey is required when identity_type is connection_key", constants.ErrInvalidParams, i)
			}
			if decoded, decErr := hex.DecodeString(r.IAPubkey); decErr != nil || len(decoded) != 32 {
				return nil, fmt.Errorf("%w: recipient %d: ia_pubkey must be a valid 32-byte hex nostr pubkey", constants.ErrInvalidParams, i)
			}
			if deps.IAChecker == nil {
				return nil, fmt.Errorf("%w: no Identity Authority trust checker configured", constants.ErrInvalidParams)
			}
			trusted, trustErr := deps.IAChecker.IsTrusted(r.IAPubkey)
			if trustErr != nil {
				return nil, fmt.Errorf("failed to check Identity Authority trust: %w", trustErr)
			}
			if !trusted {
				return nil, fmt.Errorf("%w: recipient %d: ia_pubkey is not a trusted Identity Authority", constants.ErrInvalidParams, i)
			}
		}

		sum += r.AmountMloki
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
	if int64(sum) > balance {
		return nil, fmt.Errorf("%w: insufficient balance in JIT Hub", transactions.NewInsufficientBalanceError())
	}

	return &Resolved{
		HubApp:     params.HubApp,
		Recipients: params.Recipients,
		ExpiresAt:  expiresAt,
	}, nil
}

// jitWalletScopes are the ONLY scopes ever granted to a jit_wallet child.
// Deliberately narrow: a jit_wallet's connection may be widely shared among
// its recipients, so its method surface is an explicit allowlist rather than
// a normal wallet's scope set. No pay_invoice/lookup_invoice (this app never
// makes or looks up its own invoices) and no list_transactions (which would
// leak every OTHER recipient's payout history — amount, timestamp, preimage —
// to anyone holding the shared connection). get_info stays reachable via the
// system-wide "always granted" list; get_budget is explicitly carved out of
// that same list for AppKindJITWallet (see nip47/event_handler.go) since it
// would otherwise reveal the wallet's total funded amount across every
// recipient with no proof required.
var jitWalletScopes = []string{
	constants.JIT_CLAIM_FUNDS_SCOPE,
	constants.GET_BALANCE_SCOPE,
}

// Commit creates one spend-only jit_wallet child of resolved.HubApp serving
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
		jitWalletScopes,
		db.AppKindJITWallet,
		&resolved.HubApp.ID,
		db.ParentKindJIT,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create JIT wallet app: %w", err)
	}

	// Everything from here until the transfer below is reversible bookkeeping:
	// if any of it fails, or the transfer itself fails, this defer undoes it
	// by deleting the just-created app (and its JITWalletClaim rows, via FK
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
	// keys.GetJITPairingKey/api.GetJITWalletConnection.
	pairingSecretKey, err := deps.Keys.GetJITPairingKey(newApp.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to derive JIT pairing key: %w", err)
	}
	deterministicPubKey, err := nostr.GetPublicKey(pairingSecretKey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive JIT pairing pubkey: %w", err)
	}
	if err := deps.DB.Model(&db.App{}).Where("id = ?", newApp.ID).
		Update("app_pubkey", deterministicPubKey).Error; err != nil {
		return nil, fmt.Errorf("failed to register pairing key: %w", err)
	}
	newApp.AppPubkey = deterministicPubKey
	walletPubkey := *newApp.WalletPubkey

	claimRows := make([]db.JITWalletClaim, len(resolved.Recipients))
	for i, r := range resolved.Recipients {
		claimRows[i] = db.JITWalletClaim{
			IdentityType:  r.IdentityType,
			IdentityValue: r.IdentityValue,
			IAPubkey:      r.IAPubkey,
			AmountMloki:   int64(r.AmountMloki),
		}
	}
	if err := deps.AppsService.CreateJITWalletClaims(newApp.ID, claimRows); err != nil {
		return nil, fmt.Errorf("failed to store recipient claims: %w", err)
	}

	// Transfer funds from JIT Hub to JIT Wallet. This is the one genuinely
	// irreversible step in this function, which is why it happens last: by
	// this point every other side effect is already durably committed, so
	// nothing remains that could fail and leave the wallet stranded or
	// invisible.
	invoice, err := deps.TransactionsService.MakeInvoice(
		ctx, sum, "jit transfer", "", 0,
		nil, deps.LNClient, &newApp.ID, nil, nil, nil, nil, nil, nil,
		&transactions.InternalMakeInvoiceMeta{InternalTransfer: true},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer invoice for JIT wallet: %w", err)
	}

	_, err = deps.TransactionsService.SendPaymentSync(
		invoice.PaymentRequest, nil,
		map[string]interface{}{"internal_transfer": true},
		deps.LNClient, &resolved.HubApp.ID, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to fund JIT wallet via transfer: %w", err)
	}
	fundsTransferred = true

	recipientResults := make([]RecipientResult, len(resolved.Recipients))
	for i, r := range resolved.Recipients {
		recipientResults[i] = RecipientResult{
			IdentityType:  r.IdentityType,
			IdentityValue: r.IdentityValue,
			AmountMloki:   r.AmountMloki,
		}
	}

	logger.Logger.Info().
		Uint("jit_wallet_id", newApp.ID).
		Uint("parent_app_id", resolved.HubApp.ID).
		Int("recipient_count", len(resolved.Recipients)).
		Uint64("total_mloki", sum).
		Msg("Shared JIT wallet created and funded")

	return &Result{
		WalletApp:  newApp,
		PairingURI: buildNWCPairingURI(walletPubkey, deps.RelayURLs, pairingSecretKey),
		ExpiresAt:  resolved.ExpiresAt,
		Recipients: recipientResults,
	}, nil
}

// Create resolves every recipient, creates a spend-only jit_wallet child of
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
