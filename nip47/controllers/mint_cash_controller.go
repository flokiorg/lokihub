package controllers

import (
	"context"
	"errors"
	"fmt"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/transactions"

	"gorm.io/gorm"
)

// cashRateLimitPerHour is the fallback used by tests, which build a
// config.AppConfig literal directly rather than through envconfig.Process
// (so its struct-tag default never applies). Runtime callers always go
// through controller.cfg.GetEnv().CashWalletRateLimitPerHour instead.
const cashRateLimitPerHour = 10

// mint_cash's request/response wire structs are github.com/ohstr/nmilat/nipcash's
// own exported types (nipcash.MintCashRequest/MintCashResult/RecipientParam/
// RecipientResult) — same wire shape, adopted directly instead of maintaining
// a parallel copy (nmilat migration). One accepted, tested difference:
// nipcash.MintCashResult.ExpiresAt is a plain int64 with `omitempty` (omitted
// when zero) rather than this controller's former *int64 (omitted when nil) —
// behaviorally identical for every real expiry timestamp, which is never 0.

// mapCashWalletErrorCode maps an error returned by cashwallet.Create to a NIP-47
// error code. cashwallet.Create is protocol-agnostic, so it returns plain wrapped
// errors rather than NIP-47 codes directly — this is the NWC-specific translation.
func mapCashWalletErrorCode(err error) string {
	switch {
	case errors.Is(err, transactions.NewInsufficientBalanceError()):
		return constants.ERROR_INSUFFICIENT_BALANCE
	case errors.Is(err, transactions.NewQuotaExceededError()):
		return constants.ERROR_QUOTA_EXCEEDED
	case errors.Is(err, constants.ErrInvalidParams),
		errors.Is(err, cashwallet.ErrMintIdempotencyKeyReused):
		return constants.ERROR_BAD_REQUEST
	default:
		return constants.ERROR_INTERNAL
	}
}

func (controller *nip47Controller) HandleMintCashEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc) {
	params := &nipcash.MintCashRequest{}
	resp := decodeRequest(nip47Request, params)
	if resp != nil {
		publishResponse(resp, nostr.Tags{})
		return
	}

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Int("recipient_count", len(params.Recipients)).
		Int("expiry", params.Expiry).
		Msg("Handling mint_cash request")

	// 1. App must be cash_hub kind.
	if app.Kind != db.AppKindCashHub {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "mint_cash requires a cash_hub app")
		return
	}

	// 1b. Serialize concurrent mint_cash attempts against this hub
	// (across this NWC path and the admin HTTP path, api.CreateCashWallet) so
	// two racing requests can't both pass Resolve's balance pre-check against
	// the same stale balance before either one's Commit actually transfers
	// funds out. Mirrors create_circle_wallet_controller.go's
	// activeCircleInvoices guard.
	release, ok := cashwallet.LockHub(app.ID)
	if !ok {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_NOT_READY, "wallet creation already in progress for this hub, please retry shortly")
		return
	}
	defer release()

	recipients := make([]cashwallet.RecipientInput, len(params.Recipients))
	for i, r := range params.Recipients {
		recipients[i] = cashwallet.RecipientInput{
			IdentityType:  r.IdentityType,
			IdentityValue: r.IdentityValue,
			IAPubkey:      r.IAPubkey,
			AmountMloki:   r.AmountMillis,
		}
	}

	deps := cashwallet.Deps{
		AppsService:         controller.appsService,
		TransactionsService: controller.transactionsService,
		LNClient:            controller.lnClient,
		Keys:                controller.keys,
		DB:                  controller.db,
		RelayURLs:           controller.cfg.GetRelayUrls(),
		IAChecker:           controller.iaChecker,
	}

	// 2. Validate first: identity shapes, IA trust, amount/expiry caps, and hub
	// balance are all read-only checks, so a request that was always going to
	// fail never burns rate-limit quota (mirrors create_circle_wallet_controller.go,
	// where the same ordering applies).
	resolved, err := cashwallet.Resolve(ctx, deps, cashwallet.Params{
		HubApp:         app,
		Recipients:     recipients,
		ExpirySecs:     params.Expiry,
		IdempotencyKey: params.IdempotencyKey,
	})
	if err != nil {
		respondError(publishResponse, nip47Request.Method, mapCashWalletErrorCode(err), err.Error())
		return
	}

	// 3. Rate limit per calling app pubkey (NWC-specific; the admin HTTP API has no
	// equivalent caller-facing rate limit since it's already gated by hub ownership).
	// Only requests that passed validation above reach here, so quota is spent
	// only on requests that would otherwise actually create and fund a wallet.
	if !controller.cashRateLimiter.Allow(app.AppPubkey, controller.cfg.GetEnv().CashWalletRateLimitPerHour) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RATE_LIMITED, "rate limit exceeded for mint_cash")
		return
	}

	// 4. Replay guard. Checked here rather than at the top because a request that
	// fails validation never created anything, so there is nothing to be idempotent
	// about — and an early check would record a key for a mint that never happened.
	//
	// Read-then-insert is not the guard; the unique index is. This read exists only to
	// give a legible answer, and the insert below is what actually decides, so two
	// concurrent duplicates cannot both get through.
	if params.IdempotencyKey != "" {
		var prior db.CashMintIdempotency
		err := controller.db.Where("hub_app_id = ? AND idempotency_key = ?", app.ID, params.IdempotencyKey).
			First(&prior).Error
		if err == nil {
			respondIdempotentReplay(publishResponse, nip47Request.Method, prior)
			return
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL,
				"could not check this request's idempotency key")
			return
		}
	}

	result, err := cashwallet.Commit(ctx, deps, resolved)
	if err != nil {
		respondError(publishResponse, nip47Request.Method, mapCashWalletErrorCode(err), err.Error())
		return
	}

	// The key itself was recorded inside Commit's transaction. Only the token is
	// filled in here, because it is not known until after the mint — and it is a
	// convenience for a later refusal's message, not part of the guard, so a failure
	// to store it is logged rather than surfaced. Withholding a funded wallet's token
	// to report a bookkeeping miss would strand real money.
	if params.IdempotencyKey != "" && result.CashToken != "" {
		if updErr := controller.db.Model(&db.CashMintIdempotency{}).
			Where("hub_app_id = ? AND idempotency_key = ?", app.ID, params.IdempotencyKey).
			Update("token", result.CashToken).Error; updErr != nil {
			logger.Logger.Warn().Err(updErr).
				Uint("hub_app_id", app.ID).
				Uint("cash_wallet_id", result.WalletApp.ID).
				Msg("Recorded the mint_cash idempotency key but could not attach its token; a replay will name the wallet without it")
		}
	}

	recipientResults := make([]nipcash.RecipientResult, len(result.Recipients))
	for i, r := range result.Recipients {
		recipientResults[i] = nipcash.RecipientResult{
			IdentityType:  r.IdentityType,
			IdentityValue: r.IdentityValue,
			AmountMillis:  r.AmountMloki,
			CashSecret:    r.CashSecret,
		}
	}

	logger.Logger.Info().
		Uint("cash_wallet_id", result.WalletApp.ID).
		Uint("parent_app_id", app.ID).
		Int("recipient_count", len(result.Recipients)).
		Msg("Cash wallet created and funded")

	var expiresAt int64
	if result.ExpiresAt != nil {
		expiresAt = result.ExpiresAt.Unix()
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result: nipcash.MintCashResult{
			WalletPubkey: *result.WalletApp.WalletPubkey,
			PairingURI:   result.PairingURI,
			CashToken:    result.CashToken,
			ExpiresAt:    expiresAt,
			Recipients:   recipientResults,
		},
	}, nostr.Tags{})
}

// respondIdempotentReplay answers a mint_cash whose idempotency key has already been
// served.
//
// Refused rather than answered with the original result, and that is the honest
// choice rather than a shortcut: for a cash-mode mint the secret exists only in the
// reply the caller missed — the Hub keeps a commitment, never the secret — so there is
// no original result to replay. Storing secrets to make replay work would trade this
// gap for a strictly worse one.
//
// What the caller gets instead is the wallet it already created, by pubkey and token,
// so it can read the wallet's state with cash_status rather than guess. The error code
// is BAD_REQUEST because the request genuinely cannot be served again; the message
// carries the recovery path.
func respondIdempotentReplay(publishResponse publishFunc, method string, prior db.CashMintIdempotency) {
	msg := fmt.Sprintf("this idempotency_key already minted cash wallet %s — not minting again", prior.WalletPubkey)
	if prior.Token != "" {
		msg += fmt.Sprintf(" (token %s)", prior.Token)
	}
	msg += ". A cash-mode secret from the original reply cannot be reissued; read the wallet's state with cash_status, or mint a new wallet with a different key"
	respondError(publishResponse, method, constants.ERROR_BAD_REQUEST, msg)
}
