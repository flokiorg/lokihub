package controllers

import (
	"context"
	"errors"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/jitwallet"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/transactions"
	"github.com/nbd-wtf/go-nostr"
)

// jitRateLimitPerHour is the fallback used by tests, which build a
// config.AppConfig literal directly rather than through envconfig.Process
// (so its struct-tag default never applies). Runtime callers always go
// through controller.cfg.GetEnv().JITWalletRateLimitPerHour instead.
const jitRateLimitPerHour = 10

type createJITWalletRecipientParam struct {
	IdentityType  string `json:"identity_type"` // "pubkey" | "connection_key"
	IdentityValue string `json:"identity_value"`
	IAPubkey      string `json:"ia_pubkey,omitempty"` // required iff identity_type == connection_key
	AmountMloki   uint64 `json:"amount_mloki"`
}

type createJITWalletParams struct {
	Recipients []createJITWalletRecipientParam `json:"recipients"`
	ExpirySecs int                             `json:"expiry,omitempty"`
}

type createJITWalletRecipientResult struct {
	IdentityType  string `json:"identity_type"`
	IdentityValue string `json:"identity_value"`
	AmountMloki   uint64 `json:"amount_mloki"`
}

type createJITWalletResponse struct {
	WalletPubkey string                           `json:"wallet_pubkey"`
	PairingURI   string                           `json:"pairing_uri"`
	ExpiresAt    int64                            `json:"expires_at"`
	Recipients   []createJITWalletRecipientResult `json:"recipients"`
}

// mapJITWalletErrorCode maps an error returned by jitwallet.Create to a NIP-47
// error code. jitwallet.Create is protocol-agnostic, so it returns plain wrapped
// errors rather than NIP-47 codes directly — this is the NWC-specific translation.
func mapJITWalletErrorCode(err error) string {
	switch {
	case errors.Is(err, transactions.NewInsufficientBalanceError()):
		return constants.ERROR_INSUFFICIENT_BALANCE
	case errors.Is(err, transactions.NewQuotaExceededError()):
		return constants.ERROR_QUOTA_EXCEEDED
	case errors.Is(err, constants.ErrInvalidParams):
		return constants.ERROR_BAD_REQUEST
	default:
		return constants.ERROR_INTERNAL
	}
}

func (controller *nip47Controller) HandleCreateJITWalletEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc) {
	params := &createJITWalletParams{}
	resp := decodeRequest(nip47Request, params)
	if resp != nil {
		publishResponse(resp, nostr.Tags{})
		return
	}

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Int("recipient_count", len(params.Recipients)).
		Int("expiry", params.ExpirySecs).
		Msg("Handling create_jit_wallet request")

	// 1. App must be jit_hub kind.
	if app.Kind != db.AppKindJITHub {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "create_jit_wallet requires a jit_hub app")
		return
	}

	// 1b. Serialize concurrent create_jit_wallet attempts against this hub
	// (across this NWC path and the admin HTTP path, api.CreateJITWallet) so
	// two racing requests can't both pass Resolve's balance pre-check against
	// the same stale balance before either one's Commit actually transfers
	// funds out. Mirrors create_circle_wallet_controller.go's
	// activeCircleInvoices guard.
	release, ok := jitwallet.LockHub(app.ID)
	if !ok {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "wallet creation already in progress for this hub")
		return
	}
	defer release()

	recipients := make([]jitwallet.RecipientInput, len(params.Recipients))
	for i, r := range params.Recipients {
		recipients[i] = jitwallet.RecipientInput{
			IdentityType:  r.IdentityType,
			IdentityValue: r.IdentityValue,
			IAPubkey:      r.IAPubkey,
			AmountMloki:   r.AmountMloki,
		}
	}

	deps := jitwallet.Deps{
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
	resolved, err := jitwallet.Resolve(ctx, deps, jitwallet.Params{
		HubApp:     app,
		Recipients: recipients,
		ExpirySecs: params.ExpirySecs,
	})
	if err != nil {
		respondError(publishResponse, nip47Request.Method, mapJITWalletErrorCode(err), err.Error())
		return
	}

	// 3. Rate limit per calling app pubkey (NWC-specific; the admin HTTP API has no
	// equivalent caller-facing rate limit since it's already gated by hub ownership).
	// Only requests that passed validation above reach here, so quota is spent
	// only on requests that would otherwise actually create and fund a wallet.
	if !controller.jitRateLimiter.Allow(app.AppPubkey, controller.cfg.GetEnv().JITWalletRateLimitPerHour) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RATE_LIMITED, "rate limit exceeded for create_jit_wallet")
		return
	}

	result, err := jitwallet.Commit(ctx, deps, resolved)
	if err != nil {
		respondError(publishResponse, nip47Request.Method, mapJITWalletErrorCode(err), err.Error())
		return
	}

	recipientResults := make([]createJITWalletRecipientResult, len(result.Recipients))
	for i, r := range result.Recipients {
		recipientResults[i] = createJITWalletRecipientResult{
			IdentityType:  r.IdentityType,
			IdentityValue: r.IdentityValue,
			AmountMloki:   r.AmountMloki,
		}
	}

	logger.Logger.Info().
		Uint("jit_wallet_id", result.WalletApp.ID).
		Uint("parent_app_id", app.ID).
		Int("recipient_count", len(result.Recipients)).
		Msg("JIT wallet created and funded")

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result: createJITWalletResponse{
			WalletPubkey: *result.WalletApp.WalletPubkey,
			PairingURI:   result.PairingURI,
			ExpiresAt:    result.ExpiresAt.Unix(),
			Recipients:   recipientResults,
		},
	}, nostr.Tags{})
}
