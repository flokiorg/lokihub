package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/transactions"
)

// cashRateLimitPerHour is the fallback used by tests, which build a
// config.AppConfig literal directly rather than through envconfig.Process
// (so its struct-tag default never applies). Runtime callers always go
// through controller.cfg.GetEnv().CashWalletRateLimitPerHour instead.
const cashRateLimitPerHour = 10

// mapCashWalletErrorCode maps an error returned by cashwallet to a NIP-47 error
// code. cashwallet is protocol-agnostic, so it returns plain wrapped errors
// rather than NIP-47 codes directly — this is the NWC-specific translation.
func mapCashWalletErrorCode(err error) string {
	switch {
	case errors.Is(err, cashrequest.ErrDeadlinePassed):
		return constants.ERROR_BAD_REQUEST
	case errors.Is(err, apps.ErrSliceClaimed):
		return constants.ERROR_RESTRICTED
	case errors.Is(err, cashwallet.ErrProofUsed):
		return constants.ERROR_UNAUTHORIZED
	case errors.Is(err, cashwallet.ErrRestricted):
		return constants.ERROR_RESTRICTED
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

// removedMintField names a field mint_cash no longer takes, present in the raw
// params with any value (even empty): a recipient's cash_secret, or a top-level
// idempotency_key or min_transfer_millis (NIP-CASH §Minting Cash). Decoding into
// nipcash.MintCashRequest would drop them silently.
func removedMintField(raw json.RawMessage) string {
	var params struct {
		IdempotencyKey    *json.RawMessage `json:"idempotency_key"`
		MinTransferMillis *json.RawMessage `json:"min_transfer_millis"`
		Recipients        []struct {
			CashSecret *json.RawMessage `json:"cash_secret"`
		} `json:"recipients"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return ""
	}
	if params.IdempotencyKey != nil {
		return "idempotency_key"
	}
	if params.MinTransferMillis != nil {
		return "min_transfer_millis"
	}
	for _, r := range params.Recipients {
		if r.CashSecret != nil {
			return "cash_secret"
		}
	}
	return ""
}

func (controller *nip47Controller) HandleMintCashEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, meta models.CallMeta) {
	// 1. Decode. Nothing is marked: an unreadable request names no usable id.
	params := &nipcash.MintCashRequest{}
	resp := decodeRequest(nip47Request, params)
	if resp != nil {
		publishResponse(resp, nostr.Tags{})
		return
	}

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Int("recipient_count", len(params.Recipients)).
		Int64("expiry", params.Expiry).
		Msg("Handling mint_cash request")

	// The connection must be a Cash Hub's.
	if app.Kind != db.AppKindCashHub {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "mint_cash requires a cash_hub app")
		return
	}

	// 2. [step 1] Serialise mints on this Hub (this NWC path and the admin path,
	// api.CreateCashWallet). A concurrent mint is refused, never queued.
	release, ok := cashwallet.LockHub(app.ID)
	if !ok {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RATE_LIMITED, "another mint on this Hub is in progress")
		return
	}
	defer release()

	// 3. [step 2] request_id is REQUIRED; it is marked used now, whatever else is
	// wrong with the call, so a reused id is REQUEST_REUSED and a later refusal leaves
	// it used (NIP-CASH §request_id, S-CASH-02, S-CASH-06).
	if !nipcash.ValidRequestID(params.RequestID) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST,
			"request_id is required: 32 to 64 lowercase hex characters")
		return
	}
	if err := cashrequest.New(controller.db).MarkUsed(params.RequestID, nip47Request.Method); err != nil {
		if errors.Is(err, cashrequest.ErrRequestReused) {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_REQUEST_REUSED, "this request_id was already used on this node")
			return
		}
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "could not record this request_id, please retry")
		return
	}
	// The deadline is the request's expiration: present, not passed, and at most H
	// past its arrival (NIP-CASH §Deadlines).
	if msg := mintDeadlineProblem(meta, time.Now().Unix(), controller.cfg.PrivateEnvelopeLimits().MaxRequestAgeSeconds); msg != "" {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, msg)
		return
	}

	recipients := make([]cashwallet.RecipientInput, len(params.Recipients))
	for i, r := range params.Recipients {
		recipients[i] = cashwallet.RecipientInput{
			IdentityType:  r.IdentityType,
			IdentityValue: r.IdentityValue,
			IAPubkey:      r.IAPubkey,
			AmountMloki:   r.AmountMillis,
		}
	}

	deps := controller.cashDeps()

	// 4. [steps 3 to 6] Recipients, IA trust and refused fields, expiry, and the
	// Hub's balance, in that order. All read-only, so a request that was always
	// going to fail never burns rate-limit quota.
	expiry := params.Expiry
	if expiry > constants.MAX_EXPIRY_SECS {
		expiry = constants.MAX_EXPIRY_SECS + 1 // still refused at step 5, without overflowing int
	}
	resolved, err := cashwallet.Resolve(ctx, deps, cashwallet.Params{
		HubApp:       app,
		Recipients:   recipients,
		ExpirySecs:   int(expiry),
		RefusedField: removedMintField(nip47Request.Params),
	})
	if err != nil {
		respondError(publishResponse, nip47Request.Method, mapCashWalletErrorCode(err), err.Error())
		return
	}

	// 5. Rate limit per calling app pubkey (NWC-specific; the admin HTTP API has no
	// equivalent caller-facing rate limit since it's already gated by hub ownership).
	// Only requests that passed validation above reach here, so quota is spent
	// only on requests that would otherwise actually create and fund a wallet.
	if !controller.cashRateLimiter.Allow(app.AppPubkey, controller.cfg.GetEnv().CashWalletRateLimitPerHour) {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RATE_LIMITED, "rate limit exceeded for mint_cash")
		return
	}

	// The record is written in the same commit as the bill, under the deadline
	// (NIP-CASH §The Record).
	record, err := cashwallet.MintRecord(params.RequestID, app, meta.Deadline, resolved)
	if err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "could not record this call")
		return
	}

	result, err := cashwallet.Commit(ctx, deps, resolved, record)
	if err != nil {
		respondError(publishResponse, nip47Request.Method, mapCashWalletErrorCode(err), commitErrorMessage(err))
		return
	}

	recipientResults := make([]nipcash.RecipientResult, len(result.Recipients))
	for i, r := range result.Recipients {
		recipientResults[i] = nipcash.RecipientResult{
			IdentityType:  r.IdentityType,
			IdentityValue: r.IdentityValue,
			IAPubkey:      r.IAPubkey,
			AmountMillis:  r.AmountMloki,
		}
	}

	logger.Logger.Info().
		Uint("cash_wallet_id", result.WalletApp.ID).
		Uint("parent_app_id", app.ID).
		Int("recipient_count", len(result.Recipients)).
		Msg("Cash wallet created and funded")

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result:     mintCashReply(*result.WalletApp.WalletPubkey, result.CashToken, result.ExpiresAt, recipientResults),
	}, nostr.Tags{})
}

// MintCashReply is mint_cash's result (NIP-CASH §Minting Cash, Response):
// wallet_pubkey, cash_token, expires_at (omitted for a bill that never expires)
// and the recipients echoed as sent. nipcash.MintCashResult is the client's parsed
// view and does not marshal cash_token, so the Hub writes this one.
type MintCashReply struct {
	WalletPubkey string                    `json:"wallet_pubkey"`
	CashToken    string                    `json:"cash_token"`
	ExpiresAt    *int64                    `json:"expires_at,omitempty"`
	Recipients   []nipcash.RecipientResult `json:"recipients"`
}

func mintCashReply(walletPubkey, cashToken string, expiresAt *time.Time, recipients []nipcash.RecipientResult) MintCashReply {
	out := MintCashReply{WalletPubkey: walletPubkey, CashToken: cashToken, Recipients: recipients}
	if expiresAt != nil {
		at := expiresAt.Unix()
		out.ExpiresAt = &at
	}
	return out
}

// commitErrorMessage is the reply text for a failed commit: fixed for the
// outcomes NIP-CASH names, the error itself otherwise.
func commitErrorMessage(err error) string {
	switch {
	case errors.Is(err, cashrequest.ErrDeadlinePassed):
		return "reached commit after its deadline; nothing was committed"
	case errors.Is(err, apps.ErrSliceClaimed):
		return "this slice was already claimed by another call; nothing was committed"
	case errors.Is(err, cashwallet.ErrProofUsed):
		return "a claim proof of this call was already used"
	}
	return err.Error()
}

// mintDeadlineProblem is why a mint's deadline is refused, or "" when it holds: it
// must be present and well formed, not passed at now, and no more than H past the
// request's arrival.
func mintDeadlineProblem(meta models.CallMeta, now, horizon int64) string {
	switch {
	case meta.DeadlineMalformed:
		return "the request's expiration tag is malformed"
	case !meta.DeadlinePresent:
		return "mint_cash needs an expiration tag (its deadline)"
	case nipcash.CheckDeadline(meta.Deadline, meta.ArrivedAt, horizon) != nil:
		return fmt.Sprintf("the request's expiration must lie within %d seconds of its arrival", horizon)
	case !nipcash.CommitAllowed(meta.Deadline, now):
		return "the request's expiration has passed"
	}
	return ""
}
