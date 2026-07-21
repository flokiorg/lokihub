package controllers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
	"gorm.io/gorm"
)

// circleRateLimitPerHour is the fallback used by tests, which build a
// config.AppConfig literal directly rather than through envconfig.Process
// (so its struct-tag default never applies). Runtime callers always go
// through controller.cfg.GetEnv().CircleWalletRateLimitPerHour instead.
const circleRateLimitPerHour = 3

// errCircleQuotaExceeded is a sentinel returned from inside the commitment-check
// transaction so the caller can distinguish "budget exceeded" from any other
// transaction failure and respond with the right NIP-47 error code.
var errCircleQuotaExceeded = errors.New("circle commitment would exceed available balance")

// errCircleHubBudgetExceeded is a sentinel for the hub's own admin-set budget
// ceiling (distinct from errCircleQuotaExceeded, which reflects the hub's
// real balance) — see the MaxAmountLoki check below.
var errCircleHubBudgetExceeded = errors.New("circle commitment would exceed the hub's configured budget")

// errCircleMemberAlreadyHasWallet is a sentinel for the one-active-wallet-
// per-(hub,identity) guard — see the CircleWalletMembership insert below.
var errCircleMemberAlreadyHasWallet = errors.New("identity already has an active circle wallet under this hub")

type createCircleWalletParams struct {
	RequesterPubkey string `json:"pubkey"`
	MaxAmount       uint64 `json:"max_amount"`
	Expiry          int    `json:"expiry"`
	BudgetRenewal   string `json:"budget_renewal,omitempty"`
	// IdentityEvent is the JSON-encoded kind-35521 proof that the caller
	// controls RequesterPubkey — see verifyCircleWalletIdentityEvent.
	IdentityEvent string `json:"identity_event"`
}

type createCircleWalletResponse struct {
	EncryptedPairingURI string `json:"encrypted_pairing_uri"`
	WalletPubkey        string `json:"wallet_pubkey"`
	ExpiresAt           int64  `json:"expires_at"`
	FeesPpm             int    `json:"fees_ppm"`
	BudgetRenewal       string `json:"budget_renewal"`
}

func (controller *nip47Controller) HandleCreateCircleWalletEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc) {
	params := &createCircleWalletParams{}
	resp := decodeRequest(nip47Request, params)
	if resp != nil {
		publishResponse(resp, nostr.Tags{})
		return
	}

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Str("requester", params.RequesterPubkey).
		Msg("Handling create_circle_wallet request")

	// 0. Requester pubkey must be a well-formed 64-char lowercase-hex Nostr
	// pubkey before it's used anywhere below (allowlist/following lookups,
	// rate limiting, and slicing into the wallet's display name) — an
	// unvalidated string here previously risked an out-of-range panic.
	if len(params.RequesterPubkey) != 64 || params.RequesterPubkey != strings.ToLower(params.RequesterPubkey) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "pubkey must be a 64-char lowercase-hex string")
		return
	}
	if _, err := hex.DecodeString(params.RequesterPubkey); err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "pubkey must be a 64-char lowercase-hex string")
		return
	}

	// 1. App must be circle_hub kind.
	if app.Kind != db.AppKindCircleHub {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_NOT_SUPPORTED, "create_circle_wallet requires a circle_hub app")
		return
	}

	// Load provider config from its own table.
	providerConfig, err := controller.appsService.GetCircleHubConfig(app.ID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to load Circle Hub config")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to load Circle Hub config")
		return
	}

	// 1b. Identity proof: the circle_hub connection is shared/public, so
	// params.pubkey alone proves nothing — verify the caller actually
	// controls it via a fresh, per-hub-bound, signed kind-35521 event before
	// it's used for anything (allowlist/following lookups, rate limiting).
	// This also closes the allowlist-membership oracle as a side effect: an
	// attacker without the target's private key can never reach step 2 below.
	if params.IdentityEvent == "" {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "identity_event is required")
		return
	}
	var identityEvent nostr.Event
	if err := json.Unmarshal([]byte(params.IdentityEvent), &identityEvent); err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "identity_event is not valid JSON")
		return
	}
	if err := verifyCircleWalletIdentityEvent(&identityEvent, params.RequesterPubkey, app.AppPubkey); err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, err.Error())
		return
	}
	// 1c. Single-use replay guard: a captured proof (anyone holding the
	// shared connection can decrypt every request sent over it, including
	// this one) must not be resubmittable within its own freshness window.
	if err := controller.db.Create(&db.CircleWalletIdentityProof{AppID: app.ID, EventID: identityEvent.ID}).Error; err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "identity_event has already been used")
		return
	}

	// 2. Authorization: requester must be in the circle's authorized set.
	authorized, err := controller.socialCache.IsAuthorized(ctx, params.RequesterPubkey, &providerConfig.CircleIdentity, controller.db)
	if err != nil {
		if errors.Is(err, constants.ErrSocialCacheWarmingUp) {
			// Expected right after hub startup, not a bug — the requester just
			// needs to retry once the initial cache warm-up finishes.
			respondError(publishResponse, nip47Request.Method, constants.ERROR_NOT_READY, "hub is starting up, please retry shortly")
			return
		}
		logger.Logger.Error().Err(err).Str("requester", params.RequesterPubkey).Msg("Social cache authorization check failed")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "authorization check failed")
		return
	}
	if !authorized {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "requester is not authorized by the circle policy")
		return
	}

	// 2b. One active wallet per identity per hub: cheap pre-check for the
	// common case (short-circuits before rate-limit/commitment work); the
	// authoritative, race-proof guard is the unique-constraint insert inside
	// the transaction below (step 7).
	var existingMembershipCount int64
	if err := controller.db.Model(&db.CircleWalletMembership{}).
		Where("circle_hub_app_id = ? AND requester_pubkey = ?", app.ID, params.RequesterPubkey).
		Count(&existingMembershipCount).Error; err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to check existing circle wallet membership")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "membership check failed")
		return
	}
	if existingMembershipCount > 0 {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, errCircleMemberAlreadyHasWallet.Error())
		return
	}

	// 3. Expiration: cap the caller's requested duration, or default to the
	// hub's own max when the caller omits it (an omitted/zero expiry used to
	// produce an already-expired wallet — this closes that gap).
	expirySecs := params.Expiry
	if expirySecs <= 0 {
		expirySecs = providerConfig.MaxExpSecs
	} else if providerConfig.MaxExpSecs > 0 && expirySecs > providerConfig.MaxExpSecs {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			fmt.Sprintf("expiry %d exceeds max_exp_secs %d", params.Expiry, providerConfig.MaxExpSecs))
		return
	}

	// 4. Compute the wallet's expiry from the resolved duration.
	expiresAt := time.Now().Add(time.Duration(expirySecs) * time.Second)

	// 4a. Reject a max_amount large enough to overflow int64 on the cast used
	// against commitment/balance below — unconditional, independent of
	// whether PerWalletMaxMloki happens to be configured, so this can't
	// become a balance-check bypass via uint64->int64 wraparound if a future
	// CircleHubConfig row is ever left with PerWalletMaxMloki <= 0 (today
	// prevented at creation/update time, but not a DB-level invariant).
	if params.MaxAmount > math.MaxInt64 {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, "max_amount is too large")
		return
	}

	// 4b. Budget-amount cap: the caller's requested max_amount must not
	// exceed the hub's per-wallet ceiling (independent of the aggregate
	// commitment-vs-balance check performed inside the transaction below).
	if providerConfig.PerWalletMaxMloki > 0 && params.MaxAmount > uint64(providerConfig.PerWalletMaxMloki) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_QUOTA_EXCEEDED,
			fmt.Sprintf("max_amount %d exceeds per_wallet_max_mloki %d", params.MaxAmount, providerConfig.PerWalletMaxMloki))
		return
	}

	// 4c. Budget renewal: an omitted choice defaults to "never" (always
	// compliant, regardless of the hub's floor). A requested renewal must not
	// be tighter/more frequent than the hub's configured MinBudgetRenewal
	// floor (e.g. a "monthly" floor allows "monthly"/"yearly"/"never" but
	// rejects "daily"/"weekly").
	resolvedBudgetRenewal := params.BudgetRenewal
	if resolvedBudgetRenewal == "" {
		resolvedBudgetRenewal = constants.BUDGET_RENEWAL_NEVER
	} else if !slices.Contains(constants.GetBudgetRenewals(), resolvedBudgetRenewal) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			fmt.Sprintf("budget_renewal must be one of %s, got %q", strings.Join(constants.GetBudgetRenewals(), ","), resolvedBudgetRenewal))
		return
	}
	if constants.BudgetRenewalRank(resolvedBudgetRenewal) < constants.BudgetRenewalRank(providerConfig.MinBudgetRenewal) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			fmt.Sprintf("budget_renewal %q is more frequent than this circle's min_budget_renewal %q", resolvedBudgetRenewal, providerConfig.MinBudgetRenewal))
		return
	}

	// 5. Rate limit per requester pubkey.
	if !controller.circleRateLimiter.Allow(params.RequesterPubkey, controller.cfg.GetEnv().CircleWalletRateLimitPerHour) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RATE_LIMITED, "rate limit exceeded for create_circle_wallet")
		return
	}

	// 6. In-process fast-path guard: avoids two goroutines in this same process
	// both entering the transaction below for the same provider at once. This
	// alone is not sufficient across multiple processes/instances sharing the
	// database — the Postgres advisory lock taken inside the transaction is
	// what makes the commitment check + creation atomic in that case.
	if _, existed := controller.activeCircleInvoices.LoadOrStore(app.ID, struct{}{}); existed {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "wallet creation already in progress for this provider")
		return
	}
	defer controller.activeCircleInvoices.Delete(app.ID)

	circleScopes := []string{
		constants.MAKE_INVOICE_SCOPE,
		constants.PAY_INVOICE_SCOPE,
		constants.GET_BALANCE_SCOPE,
		constants.LOOKUP_INVOICE_SCOPE,
		constants.LIST_TRANSACTIONS_SCOPE,
		constants.GET_INFO_SCOPE,
	}

	// 7. Commitment check + wallet creation as one atomic unit. On Postgres, an
	// advisory lock scoped to this transaction serialises concurrent requests
	// for the same provider across processes — the lock is released automatically
	// when the transaction commits or rolls back, so a failed/quota-exceeded
	// attempt never leaves anything held.
	var newApp *db.App
	var pairingSecretKey string
	err = controller.db.Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock($1)", int64(app.ID)).Error; err != nil {
				return fmt.Errorf("acquire circle commitment lock: %w", err)
			}
		}

		commitment, err := queries.GetCircleCommitmentMloki(tx, app.ID)
		if err != nil {
			return fmt.Errorf("commitment check failed: %w", err)
		}
		balance := queries.GetIsolatedBalance(tx, app.ID)
		if commitment+int64(params.MaxAmount) > balance {
			return errCircleQuotaExceeded
		}

		// Hub-wide budget ceiling: an optional admin-set cap on the combined
		// max_amount of every currently-live child, independent of the hub's
		// real balance above. Stored on the hub's own circle_wallet-scope
		// AppPermission (the hub is never granted pay_invoice, so this is not
		// the standard payment-budget mechanism — see plan doc for context).
		var hubPermission db.AppPermission
		if err := tx.Where("app_id = ? AND scope = ?", app.ID, constants.CIRCLE_WALLET_SCOPE).
			First(&hubPermission).Error; err != nil {
			return fmt.Errorf("failed to load hub permission: %w", err)
		}
		if hubPermission.MaxAmountLoki > 0 && commitment+int64(params.MaxAmount) > int64(hubPermission.MaxAmountLoki)*1000 {
			return errCircleHubBudgetExceeded
		}

		createdApp, secret, err := controller.appsService.CreateAppTx(tx,
			apps.GenerateChildName(app.Name, params.RequesterPubkey),
			"",
			params.MaxAmount/1000,
			resolvedBudgetRenewal,
			&expiresAt,
			circleScopes,
			db.AppKindCircleWallet,
			&app.ID,
			db.ParentKindCircle,
			map[string]interface{}{"requester_pubkey": params.RequesterPubkey},
		)
		if err != nil {
			return err
		}

		// One active wallet per identity per hub, atomic guard: a unique-
		// constraint violation here means a concurrent request for the same
		// identity won the race since the pre-check above — roll back the
		// whole transaction, including the App/permission rows just created.
		if err := tx.Create(&db.CircleWalletMembership{
			CircleHubAppID:  app.ID,
			RequesterPubkey: params.RequesterPubkey,
			WalletAppID:     createdApp.ID,
		}).Error; err != nil {
			return errCircleMemberAlreadyHasWallet
		}

		newApp, pairingSecretKey = createdApp, secret
		return nil
	})

	if errors.Is(err, errCircleQuotaExceeded) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_QUOTA_EXCEEDED, errCircleQuotaExceeded.Error())
		return
	}
	if errors.Is(err, errCircleHubBudgetExceeded) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_QUOTA_EXCEEDED, errCircleHubBudgetExceeded.Error())
		return
	}
	if errors.Is(err, errCircleMemberAlreadyHasWallet) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, errCircleMemberAlreadyHasWallet.Error())
		return
	}
	if err != nil {
		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Error:      mapNip47Error(err),
		}, nostr.Tags{})
		return
	}

	// The outer transaction above has now committed, so newApp is durably
	// visible to other connections — only now is it safe to publish
	// nwc_app_created (see CreateAppTx's doc comment). Publishing any earlier
	// would race createAppConsumer's event-driven lookup against this
	// transaction's own commit, silently skipping the new wallet's relay
	// subscription setup with no retry — a wallet that would then never
	// receive any NIP-47 request for its entire lifetime.
	controller.appsService.NotifyAppCreated(newApp)

	// Encrypt pairing URI for the requester using NIP-44.
	walletPubkey := *newApp.WalletPubkey
	pairingURI := buildNWCPairingURI(walletPubkey, controller.cfg.GetRelayUrls(), pairingSecretKey)

	circleWalletPrivKey, err := controller.keys.GetAppWalletKey(newApp.ID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("circle_wallet_id", newApp.ID).Msg("Failed to get circle wallet private key")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to derive wallet key")
		return
	}

	encryptedURI, err := encryptPairingURI(params.RequesterPubkey, circleWalletPrivKey, pairingURI)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to encrypt pairing URI for circle wallet")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to encrypt pairing URI")
		return
	}

	logger.Logger.Info().
		Uint("circle_wallet_id", newApp.ID).
		Uint("parent_app_id", app.ID).
		Msg("Circle wallet created")

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result: createCircleWalletResponse{
			EncryptedPairingURI: encryptedURI,
			WalletPubkey:        walletPubkey,
			ExpiresAt:           expiresAt.Unix(),
			FeesPpm:             providerConfig.FeesPpm,
			BudgetRenewal:       resolvedBudgetRenewal,
		},
	}, nostr.Tags{})
}
