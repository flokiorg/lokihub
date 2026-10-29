package controllers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flokiorg/lokihub/hubclock"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipIC"
	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	decodepay "github.com/flokiorg/lokihub/decodepay"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/transactions"
)

const (
	// nostrKindClaimProof is a recipient's per-claim proof of identity. Unlike
	// the old kind-35521 "identity declaration" (a static, reusable
	// declaration of connection_key ownership) — and unlike NIP-IC's own
	// kind 35521 (Identity Connection), a genuinely different, similarly
	// long-lived claim this event was never compatible with despite sharing
	// its number — this event is signed fresh for each cash_redeem call and
	// is bound to one specific wallet AND one specific invoice (see
	// verifyClaimIdentityEvent) — a captured/intercepted copy of it is
	// useless for any invoice other than the one it was signed for, which
	// matters here because a cash_wallet's connection is meant to be
	// shared/public, so anyone holding it can decrypt every cash_redeem
	// request sent on it, including other recipients'. Kind 23198 (not
	// 35521): this proof is never independently published to a relay — it
	// only ever travels embedded inside an already end-to-end-encrypted
	// NIP-47 request body — so it belongs in the ephemeral range
	// (20000-29999), not the addressable/parameterized-replaceable range
	// (30000-39999) NIP-IC's long-lived claim correctly uses. 23198 sits
	// directly adjacent to NIP-47's own 23194-23197 block, since this NIP
	// depends on NIP-47. Aliased to nipcash's own exported constant (nmilat
	// migration) rather than a re-hardcoded literal, so this value
	// can never drift from the protocol layer's own definition of it.
	nostrKindClaimProof = nipcash.KindClaimProof
	// nostrKindIAAttestation is unchanged from the old design: an Identity
	// Authority's signed attestation that a given nostr pubkey owns a given
	// connection_key. Only used for identity_type == connection_key. Aliased
	// to nipIC's own exported constant (nmilat migration) — this is
	// genuinely NIP-IC's own kind, not NIP-CASH's, so it borrows nipIC's name
	// for it directly rather than redeclaring the number.
	nostrKindIAAttestation = nipIC.KindAttestation
)

// cashRedeemForbiddenParams are fields a caller may never set on cash_redeem:
// the fee, the rate and same-node-ness are the Hub's to decide (NIP-CASH §The
// Redeem Fee). A key equal to one of these, or starting with "fee" or
// "redeem_fee", is BAD_REQUEST.
var cashRedeemForbiddenParams = map[string]bool{"same_node": true, "rate": true}

func cashRedeemForbiddenParam(raw json.RawMessage) (string, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return "", false
	}
	for k := range fields {
		if cashRedeemForbiddenParams[k] || strings.HasPrefix(k, "fee") || strings.HasPrefix(k, "redeem_fee") {
			return k, true
		}
	}
	return "", false
}

// cashRedeemOutcome is what the payment of a redeem came back with.
type cashRedeemOutcome struct {
	tx  *transactions.Transaction
	err error
}

// HandleCashRedeemEvent serves cash_redeem in NIP-CASH §Redeeming a Slice's
// order: locate the slice (NOT_FOUND, RESTRICTED), authorize it (UNAUTHORIZED,
// RESTRICTED), the invoice policy (RESTRICTED) and amount rules (BAD_REQUEST,
// RESTRICTED for a zero payout), the late-failure cool-down and the in-flight
// bound (RATE_LIMITED), then the first-wins claim, which consumes the claim
// proof in the same transaction, and the payment.
//
// The answer is given while the payment may still settle: after
// CASH_REDEEM_REPLY_WAIT_SECS (or sooner when the envelope's deadline is nearer)
// the item is answered INTERNAL with the slice kept claimed, and the payment's
// outcome is applied when it comes. A payment that fails before it can have
// settled is PAYMENT_FAILED and releases the claim and the proof in one
// transaction.
func (controller *nip47Controller) HandleCashRedeemEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, tags nostr.Tags, call *PrivateCall) {
	params := &nipcash.CashRedeemRequest{}
	resp := decodeRequest(nip47Request, params)
	if resp != nil {
		publishResponse(resp, tags)
		return
	}
	method := nip47Request.Method
	refuse := func(code, message string) { respondError(publishResponse, method, code, message) }

	if app.Kind != db.AppKindCashWallet {
		refuse(constants.ERROR_RESTRICTED, "cash_redeem requires a cash_wallet app")
		return
	}
	if field, forbidden := cashRedeemForbiddenParam(nip47Request.Params); forbidden {
		refuse(constants.ERROR_BAD_REQUEST, fmt.Sprintf("%s is the Hub's to decide and may not be sent", field))
		return
	}

	// The credential's shape: a cash secret, or an identity with its claim proof,
	// never both.
	isCash := params.CashSecret != ""
	if isCash {
		if params.IdentityType != "" || params.IdentityValue != "" || params.IdentityEvent != "" || params.AttestationEvent != "" {
			refuse(constants.ERROR_BAD_REQUEST,
				"cash_secret is mutually exclusive with identity_type, identity_value, identity_event, and attestation_event")
			return
		}
		if !nipcash.ValidCashSecret(params.CashSecret) {
			refuse(constants.ERROR_BAD_REQUEST, "cash_secret must be 64 lowercase hex characters")
			return
		}
	} else {
		if params.IdentityType == "" || params.IdentityValue == "" || params.IdentityEvent == "" {
			refuse(constants.ERROR_BAD_REQUEST, "invoice, identity_type, identity_value, and identity_event are all required")
			return
		}
		if params.IdentityType != db.CashIdentityPubkey && params.IdentityType != db.CashIdentityConnectionKey {
			refuse(constants.ERROR_BAD_REQUEST,
				fmt.Sprintf("identity_type must be %q or %q", db.CashIdentityPubkey, db.CashIdentityConnectionKey))
			return
		}
		if params.IdentityType == db.CashIdentityConnectionKey && params.AttestationEvent == "" {
			refuse(constants.ERROR_BAD_REQUEST, "attestation_event is required when identity_type is connection_key")
			return
		}
	}
	if params.Invoice == "" {
		refuse(constants.ERROR_BAD_REQUEST, "invoice is required")
		return
	}
	bolt11 := strings.ToLower(params.Invoice)
	invoice, err := decodepay.Decode(bolt11)
	if err != nil {
		refuse(constants.ERROR_BAD_REQUEST, "invoice is not a valid Lightning invoice")
		return
	}

	logger.Logger.Info().Uint("app_id", app.ID).Str("identity_type", params.IdentityType).Bool("cash", isCash).
		Msg("Handling cash_redeem request")

	// Step 1: locate.
	claim, code, message := controller.locateSlice(app, isCash, params.IdentityType, params.IdentityValue)
	if code != "" {
		refuse(code, message)
		return
	}

	// Step 2: authorize.
	var limitKey, proofID string
	var proofCreatedAt int64
	if isCash {
		commitment, err := nipcash.CashCommitment(params.CashSecret)
		if err != nil || subtle.ConstantTimeCompare([]byte(commitment), []byte(claim.IdentityValue)) != 1 {
			refuse(constants.ERROR_UNAUTHORIZED, "this cash_secret does not open this slice")
			return
		}
		limitKey = claim.IdentityValue
	} else {
		verdict, err := controller.verifyRedeemClaimProof(app, claim, params, invoice.PaymentHash)
		if err != nil {
			var claimErr *nipcash.ClaimError
			if errors.As(err, &claimErr) && claimErr.Code == constants.ERROR_RESTRICTED {
				refuse(constants.ERROR_RESTRICTED, "the Identity Authority for this claim is no longer trusted")
				return
			}
			refuse(constants.ERROR_UNAUTHORIZED, "identity_event does not authorize this redeem: "+claimReason(err))
			return
		}
		// The item proof and the claim proof must be one signer's (NIP-CASH §Claim
		// Proofs), or whoever assembled the item could re-point a captured proof.
		if call == nil || !nipcash.ItemSignerMatchesClaim(call.ItemSigner, verdict) {
			refuse(constants.ERROR_UNAUTHORIZED, "the item proof was not signed by the claim proof's signer")
			return
		}
		limitKey = verdict.Signer
		proofID = verdict.RecomputedID
		proofCreatedAt = eventCreatedAt(params.IdentityEvent)
	}
	if !controller.claimCallAllowed(limitKey) {
		refuse(constants.ERROR_RATE_LIMITED, "too many calls by this signer; retry later")
		return
	}

	// Step 3: the invoice policy, then the amount rules.
	env := controller.cfg.GetEnv()
	if limit := env.CashRedeemMaxInvoiceExpirySecs; limit > 0 && invoice.Expiry > int64(limit) {
		refuse(constants.ERROR_RESTRICTED, fmt.Sprintf("the invoice's expiry exceeds this Hub's limit of %d seconds", limit))
		return
	}
	if limit := env.CashRedeemMaxFinalCLTV; limit > 0 && invoice.MinFinalCLTVExpiry > int64(limit) {
		refuse(constants.ERROR_RESTRICTED, fmt.Sprintf("the invoice's final CLTV delta exceeds this Hub's limit of %d", limit))
		return
	}
	committed := uint64(claim.AmountMloki) //nolint:gosec // non-negative
	sameNode := transactions.IsSelfPayment(controller.db, invoice, controller.lnClient)
	fee := uint64(0)
	if !sameNode {
		fee, _, err = nipcash.RedeemFee(committed, uint64(max(claim.RedeemFeeBaseMloki, 0)), uint64(max(claim.RedeemFeePpm, 0))) //nolint:gosec // non-negative
		if err != nil {
			refuse(constants.ERROR_INTERNAL, "this slice's redeem fee is invalid")
			return
		}
	}
	var invoiceMillis *uint64
	if invoice.AmountMloki != 0 {
		v := uint64(invoice.AmountMloki) //nolint:gosec // positive
		invoiceMillis = &v
	}
	required, check := nipcash.RequiredInvoiceAmount(committed, fee, sameNode, invoiceMillis, params.Amount)
	switch {
	case check.Code == constants.ERROR_RESTRICTED:
		refuse(constants.ERROR_RESTRICTED, fmt.Sprintf(
			"this slice cannot be redeemed to Lightning: its %d millis are fully consumed by the %d millis redeem fee, so the payout would be zero. Consolidate it with another slice first, or redeem to an invoice on this node, where the fee is waived",
			committed, fee))
		return
	case check.Code != "" && invoiceMillis != nil && params.Amount != nil:
		refuse(constants.ERROR_BAD_REQUEST, "amount must not be sent with an invoice that carries an amount")
		return
	case check.Code != "":
		message := fmt.Sprintf("the invoice must be for exactly %d millis (this slice's %d minus its %d millis redeem fee)", required, committed, fee)
		if sameNode {
			message = fmt.Sprintf("this redemption resolves to a same-node payment, which is fee-free: the invoice must be for the full %d millis", committed)
		}
		refuse(constants.ERROR_BAD_REQUEST, message)
		return
	}

	// Step 4: the late-failure cool-down, then the in-flight bound and the claim,
	// in one transaction with the consumed proof.
	now := hubclock.Now()
	if claim.RedeemCooldownUntil != nil && now.Before(*claim.RedeemCooldownUntil) {
		refuse(constants.ERROR_RATE_LIMITED, "this slice's last payout failed; try later")
		return
	}
	hubID := uint(0)
	if app.ParentAppID != nil {
		hubID = *app.ParentAppID
	}
	err = controller.appsService.ClaimSliceForRedeem(apps.RedeemClaim{
		Slice:          apps.SliceRef{ClaimID: claim.ID, IdentityType: claim.IdentityType, IdentityValue: claim.IdentityValue, TransferCount: claim.TransferCount},
		HubAppID:       hubID,
		PaymentHash:    invoice.PaymentHash,
		ProofID:        proofID,
		ProofCreatedAt: proofCreatedAt,
		InflightPerHub: env.CashRedeemInflightPerHub,
		InflightNode:   env.CashRedeemInflightNode,
	}, now)
	switch {
	case errors.Is(err, apps.ErrRedeemInflight):
		refuse(constants.ERROR_RATE_LIMITED, "too many redemptions are in flight; retry later")
		return
	case errors.Is(err, apps.ErrSliceClaimed):
		refuse(constants.ERROR_RESTRICTED, "this slice was already claimed by another call")
		return
	case errors.Is(err, cashrequest.ErrProofConsumed):
		refuse(constants.ERROR_UNAUTHORIZED, "this claim proof was already used")
		return
	case err != nil:
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to claim a slice for cash_redeem")
		refuse(constants.ERROR_INTERNAL, "failed to start the redemption")
		return
	}

	// The route is capped by the quoted fee plus a margin, for every external
	// redemption.
	metadata := map[string]interface{}{"cash_claim_slice": true, "cash_redeem_fee_mloki": fee}
	if !sameNode {
		margin := committed / 1_000_000 * uint64(max(env.CashRedeemRouteMarginPpm, 0)) //nolint:gosec // non-negative
		margin += committed % 1_000_000 * uint64(max(env.CashRedeemRouteMarginPpm, 0)) / 1_000_000
		margin = max(margin, uint64(max(env.CashRedeemRouteMarginMinMillis, 0))) //nolint:gosec // non-negative
		metadata[transactions.CashRedeemRouteCapKey] = fee + margin
	}
	// Only an amountless invoice takes the caller's amount.
	var payAmount *uint64
	if invoiceMillis == nil {
		payAmount = params.Amount
	}

	apps.BeginRedeemAnswer(invoice.PaymentHash)
	done := make(chan cashRedeemOutcome, 1)
	go func() {
		tx, err := controller.transactionsService.SendPaymentSync(bolt11, payAmount, metadata, controller.lnClient, &app.ID, &requestEventId)
		done <- cashRedeemOutcome{tx: tx, err: err}
	}()

	timer := time.NewTimer(controller.redeemReplyWait(call))
	defer timer.Stop()
	select {
	case outcome := <-done:
		code, message, result := controller.finishRedeem(app, claim, invoice.PaymentHash, fee, outcome, false)
		if code != "" {
			refuse(code, message)
			return
		}
		publishResponse(&models.Response{ResultType: method, Result: result}, tags)
	case <-timer.C:
		// No second reply is ever sent: the outcome is read through cash_status.
		refuse(constants.ERROR_INTERNAL, "the payment's outcome is not known yet; this slice stays claimed until it settles or fails")
		go func() {
			controller.finishRedeem(app, claim, invoice.PaymentHash, fee, <-done, true)
		}()
	}
}

// redeemReplyWait is how long cash_redeem waits for its payment before it
// answers: CASH_REDEEM_REPLY_WAIT_SECS, at most until the call's deadline.
func (controller *nip47Controller) redeemReplyWait(call *PrivateCall) time.Duration {
	secs := controller.cfg.GetEnv().CashRedeemReplyWaitSecs
	if secs <= 0 {
		secs = 20
	}
	wait := time.Duration(secs) * time.Second
	if call != nil && call.Meta.DeadlinePresent {
		left := time.Until(time.Unix(call.Meta.Deadline, 0))
		wait = min(wait, max(left, time.Second))
	}
	return wait
}

// finishRedeem applies a redeem payment's outcome and, when the redeem is still
// answering (late false), says what to answer: a code and message, or the
// result. late means the item was already answered INTERNAL.
//
//   - pending, or sent but not recorded: the slice stays claimed; the payment's
//     own events settle or release it (INTERNAL).
//   - any other failure: nothing is in flight, so the claim and its proof are
//     released in one transaction (PAYMENT_FAILED); a late one sets the
//     cool-down.
//   - settled without a preimage: claimed for good (INTERNAL).
//   - settled: the payout facts are recorded on the slice.
func (controller *nip47Controller) finishRedeem(app *db.App, claim *db.CashWalletClaim, paymentHash string, fee uint64, outcome cashRedeemOutcome, late bool) (string, string, any) {
	defer apps.EndRedeemAnswer(paymentHash)
	err := outcome.err
	switch {
	case err != nil && (errors.Is(err, lnclient.ErrPaymentPending) || errors.Is(err, transactions.ErrPaymentSentNotRecorded)):
		logger.Logger.Warn().Err(err).Uint("app_id", app.ID).Str("payment_hash", paymentHash).
			Msg("cash_redeem payment outcome unknown; slice held until it resolves")
		// A failure published while this redeem held the hash was left to it.
		apps.EndRedeemAnswer(paymentHash)
		if _, relErr := controller.appsService.ReleaseCashSliceForFailedPayment(app.ID, paymentHash); relErr != nil {
			logger.Logger.Error().Err(relErr).Uint("app_id", app.ID).Msg("Failed to recheck a pending cash_redeem payout")
		}
		return constants.ERROR_INTERNAL, "the payment's outcome is not known yet; this slice stays claimed until it settles or fails", nil
	case err != nil:
		var cooldown *time.Time
		if late {
			cooldown = controller.appsService.LateFailureCooldown(time.Now())
		}
		released, relErr := controller.appsService.ReleaseRedeemClaim(app.ID, paymentHash, cooldown)
		if relErr != nil || !released {
			// The ledger did not record the failure (it still shows the payment
			// pending), or the release itself failed: the slice stays claimed
			// until the payment's own events or the orphan sweep resolve it.
			logger.Logger.Error().Err(relErr).AnErr("payment_error", err).Uint("app_id", app.ID).Str("payment_hash", paymentHash).
				Msg("cash_redeem payment failed but its slice could not be released yet")
			return constants.ERROR_INTERNAL, "the payment failed, but this slice stays claimed until the failure is recorded", nil
		}
		logger.Logger.Warn().Err(err).Uint("app_id", app.ID).Str("payment_hash", paymentHash).Bool("late", late).
			Msg("cash_redeem payment failed; slice claimable again")
		return constants.ERROR_PAYMENT_FAILED, "the payment failed; this slice can be redeemed again", nil
	case outcome.tx == nil || outcome.tx.Preimage == nil:
		logger.Logger.Error().Uint("app_id", app.ID).Str("payment_hash", paymentHash).
			Msg("cash_redeem payment settled without a preimage; slice kept claimed")
		controller.maybeAutoDeleteDrainedCashWallet(app)
		return constants.ERROR_INTERNAL, "the payment was sent but could not be recorded; this slice stays claimed", nil
	}

	tx := outcome.tx
	if err := controller.appsService.SetCashSliceRedeemPayment(app.ID, claim.IdentityType, claim.IdentityValue, apps.CashSliceRedeemPayment{
		PaymentHash:     tx.PaymentHash,
		Preimage:        *tx.Preimage,
		RedeemFeeMloki:  int64(fee),         //nolint:gosec // at most the slice's amount
		RoutingFeeMloki: int64(tx.FeeMloki), //nolint:gosec // a routing fee the node reports
		SettledAt:       tx.SettledAt,
	}); err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to record cash_redeem payout facts on the slice")
	}
	logger.Logger.Info().Uint("app_id", app.ID).Str("identity_type", claim.IdentityType).Msg("Cash wallet slice redeemed")
	// A redeem that took the bill's last slice archives and deletes it, after the
	// answer (NIP-CASH §Lifecycle and Deletion).
	defer controller.maybeAutoDeleteDrainedCashWallet(app)
	// fees_paid is the recipient's own redeem fee, never the routing cost.
	return "", "", payResponse{Preimage: *tx.Preimage, FeesPaid: fee}
}

// verifyRedeemClaimProof verifies a redeem's kind-23198 claim proof with nmilat
// (NIP-CASH §Claim Proofs): its exact tags, bound to this wallet, m cash_redeem
// and the invoice's payment hash, fresh, unused, and signed by the slice's
// identity (for a connection_key slice through a live attestation by the IA
// recorded on it, checked last for trust).
func (controller *nip47Controller) verifyRedeemClaimProof(app *db.App, claim *db.CashWalletClaim, params *nipcash.CashRedeemRequest, paymentHash string) (nipcash.ClaimVerdict, error) {
	wallet := ""
	if app.WalletPubkey != nil {
		wallet = *app.WalletPubkey
	}
	store := cashrequest.New(controller.db)
	return nipcash.VerifyClaimProof(params.IdentityEvent, nipcash.ClaimExpectation{
		Binding:     nipcash.ClaimBinding{Method: nipcash.MethodCashRedeem, Wallet: wallet, Bolt11Hash: paymentHash},
		SliceType:   claim.IdentityType,
		SliceValue:  claim.IdentityValue,
		SliceIA:     claim.IAPubkey,
		Attestation: params.AttestationEvent,
		IATrusted: func(ia string) bool {
			trusted, err := controller.iaChecker.IsTrusted(ia)
			if err != nil {
				logger.Logger.Error().Err(err).Msg("Failed to check Identity Authority trust")
			}
			return err == nil && trusted
		},
		Consumed: func(id string) bool {
			used, err := store.IsProofConsumed(id)
			return err == nil && used
		},
	}, time.Now())
}
