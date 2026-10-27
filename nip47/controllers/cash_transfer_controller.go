package controllers

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// cashTransferNewIdentityParam/cashTransferParams below are no longer used to
// parse the real request (HandleCashTransferEvent decodes straight into
// github.com/ohstr/nmilat/nipcash's own exported CashTransferRequest now -
// nmilat migration) - they're kept purely as this package's own test
// fixtures. nipcash.CashTransferRequest's own NewIdentity field type is
// unexported inside nipcash (nmilat's own internal wire-shape detail), so a
// test can't build one as `nipcash.CashTransferRequest{NewIdentity:
// SomeType{...}}` in a single literal; every existing test here instead
// builds this local, structurally-identical type and marshals it to JSON
// (see handleCashTransferFor), which is exactly how a real NWC client's
// request arrives anyway - so nothing downstream needs to change.
type cashTransferNewIdentityParam struct {
	IdentityType  string `json:"identity_type"` // "pubkey" | "connection_key" | "cash"
	IdentityValue string `json:"identity_value,omitempty"`
	IAPubkey      string `json:"ia_pubkey,omitempty"` // required iff identity_type == connection_key
}

type cashTransferParams struct {
	// The caller's CURRENT registered identity — mutually exclusive with
	// CashSecret. IdentityEvent is the JSON-encoded kind-23198 transfer
	// proof, signed fresh for this call and bound to this wallet + this
	// specific new_identity (see verifyTransferIdentityEvent).
	IdentityType     string `json:"identity_type,omitempty"`
	IdentityValue    string `json:"identity_value,omitempty"`
	IdentityEvent    string `json:"identity_event,omitempty"`
	AttestationEvent string `json:"attestation_event,omitempty"`
	// CashSecret authenticates as the slice's CURRENT cash-mode identity, in
	// place of every field above — a cash-mode slice has no identity capable
	// of signing a proof event, so presenting its secret is the entire proof
	// (mirrors cash_redeem's own cash-mode path).
	CashSecret string `json:"cash_secret,omitempty"`

	NewIdentity cashTransferNewIdentityParam `json:"new_identity"`

	// AmountMillis is OPTIONAL — omitted, or equal to the slice's current full
	// amount, means "transfer it all" (this method's only behavior before
	// splitting existed). A value LESS than the slice's current amount
	// splits off exactly that much into a brand-new dedicated cash_wallet,
	// leaving the remainder behind on THIS slice, under the SAME current
	// identity, otherwise untouched — the "$20 bill, hand over $5, keep $15
	// in change" model (NIP-CASH §Transferring and Splitting a Slice). The carved-off amount,
	// and any nonzero remainder left behind, must each be at least this
	// slice's own MinTransferMloki (0 = no floor) — enforced by
	// handleCashTransferSplit.
	AmountMillis *uint64 `json:"amount_millis,omitempty"`

	// ZapTarget makes this a zap transfer (NIP-CZ); nil when absent.
	ZapTarget *string `json:"zap_target,omitempty"`

	// RemainderCommitment is the fresh commitment a partial split of a cash
	// slice gives its remainder.
	RemainderCommitment string `json:"remainder_commitment,omitempty"`

	// RequestID is REQUIRED (NIP-CASH §request_id).
	RequestID string `json:"request_id,omitempty"`
}

// PrivateCall is what the private transport knows about an item beyond its
// params: its call metadata (the deadline is the envelope's not_after) and the
// pubkey that signed its item proof, "" for a proofless item.
type PrivateCall struct {
	Meta       models.CallMeta
	ItemSigner string
}

// cashTransferResponse never carries a secret of any kind — IdentityValue is
// always either a public identity (pubkey/connection_key) or a one-way
// commitment the caller already supplied for a cash-mode target, deliberately
// never a value the wallet itself generated (see the cash-mode branch of
// HandleCashTransferEvent for why: this response travels over the shared
// cash_wallet connection, decryptable by every recipient who ever held it).
type cashTransferResponse struct {
	AmountMillis  uint64 `json:"amount_millis"`
	IdentityType  string `json:"identity_type"`
	IdentityValue string `json:"identity_value,omitempty"`
	// RemainingAmountMillis is populated only when this call went through the
	// split path (see NewWalletPubkey/NewWalletToken below): 0 for a full
	// split, >0 for a partial one — so the caller's own client can update
	// its cached view of what's left on THIS connection without a separate
	// cash_status round-trip. Never populated for an in-place
	// reassignment (nothing was carved off; the whole slice just changed
	// hands, still for its original, unchanged amount).
	RemainingAmountMillis *uint64 `json:"remaining_amount_millis,omitempty"`
	// NewWalletPubkey and NewWalletToken are populated only when this
	// transfer split the slice's value — full or partial — off into a
	// brand-new dedicated cash_wallet instead of reassigning identity in
	// place. See HandleCashTransferEvent's routing comment for exactly when
	// each outcome applies.
	//
	// NewWalletPubkey is the new wallet's own WalletPubkey, in the clear —
	// safe to return unencrypted since a bare pubkey with no secret grants
	// nothing (same reasoning as create_circle_wallet_controller.go's own
	// plaintext WalletPubkey field). It exists so the recipient can derive
	// the correct decryption key for NewWalletToken: a fresh one-off keypair
	// generated only for this response would never reach them any other way.
	//
	// NewWalletToken is that new wallet's lokicash1... connection token,
	// NIP-44 encrypted to the pubkey that signed this call's identity_event
	// using the new wallet's own keypair (NewWalletPubkey / the matching
	// server-held privkey) — a second, inner encryption layer nested inside
	// this response's own normal per-connection encryption. Every
	// co-recipient of THIS wallet's shared connection can still decrypt the
	// outer response same as always, but only the caller who just proved
	// ownership of the slice being split can decrypt this field. See
	// NIP-CASH "Splitting a Slice Off Into a Dedicated Wallet".
	NewWalletPubkey string `json:"new_wallet_pubkey,omitempty"`
	NewWalletToken  string `json:"new_wallet_token,omitempty"`
	// RemainderWalletPubkey/RemainderWalletToken carry the caller's own change,
	// now in its OWN fresh dedicated wallet, for a PARTIAL split (NIP-CASH
	// §Transferring and Splitting a Slice: a partial split consumes the source and mints two
	// wallets — carved for new_identity, remainder for the caller — so a
	// wallet's committed amount is never rewritten in place). Absent for a full
	// transfer (no remainder) and for an in-place reassignment. Delivered the
	// same nested-encrypted way as NewWalletToken.
	RemainderWalletPubkey string `json:"remainder_wallet_pubkey,omitempty"`
	RemainderWalletToken  string `json:"remainder_wallet_token,omitempty"`
	// IssuedAt and ExpiresAt are a zap transfer's zap bill's, exactly as signed,
	// and present only on a zap transfer; ExpiresAt is 0, not omitted, for a bill
	// that never expires (NIP-CASH §Transferring and Splitting a Slice, Response).
	IssuedAt  *uint64 `json:"issued_at,omitempty"`
	ExpiresAt *uint64 `json:"expires_at,omitempty"`
}

// newIdentityHash binds a transfer proof to a specific target identity —
// the same role bolt11_hash plays for a claim proof (see
// verifyClaimIdentityEvent's doc comment). identityValue is "" for a cash-mode
// target, since the caller doesn't choose (or know) a cash-mode target's value
// ahead of generating it. iaPubkey is included so a connection_key target's
// Identity Authority is part of what the signer committed to — omitting it
// let a captured proof be resubmitted with a
// DIFFERENT, still-trusted IA swapped in, redirecting who is authoritative to
// redeem the transferred slice even though identity_value (the connection_key
// string itself) never changed. Always "" for pubkey/cash-mode targets, which
// have no ia_pubkey concept, so their hash is unaffected by this field.
// noSliceRegisteredMessage is the NOT_FOUND message for a claim lookup that
// found no UNCLAIMED slice for (walletAppID, identityType, identityValue) —
// shared by cash_transfer and cash_redeem. Best-effort enriches the generic
// message with a breadcrumb when the slice DOES exist but was already split
// off into a new dedicated wallet: exactly the situation a client built
// against the pre-two-wallet-split contract hits on its natural retry for a
// "remainder" it believes it still holds on this connection.
// This doesn't fix that client's underlying assumption, but turns a silent
// dead end into an actionable one. Diagnostic-only: any failure in the lookup
// itself silently falls back to the generic message rather than affecting
// the response.
func (controller *nip47Controller) noSliceRegisteredMessage(walletAppID uint, identityType, identityValue string) string {
	const generic = "no slice registered for this identity"
	var priorClaim db.CashWalletClaim
	err := controller.db.Where(
		"wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NOT NULL AND spun_off_to_wallet_app_id IS NOT NULL",
		walletAppID, identityType, identityValue,
	).First(&priorClaim).Error
	if err != nil {
		return generic
	}
	return generic + " — its value was already split off into a new dedicated wallet by an earlier call; " +
		"check cash_status or that earlier call's own response for the new connection, this one no longer holds it"
}

func newIdentityHash(identityType, identityValue, iaPubkey string) string {
	sum := sha256.Sum256([]byte(identityType + ":" + identityValue + ":" + iaPubkey))
	return hex.EncodeToString(sum[:])
}

func (controller *nip47Controller) HandleCashTransferEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, tags nostr.Tags, call *PrivateCall) {
	params := &nipcash.CashTransferRequest{}
	resp := decodeRequest(nip47Request, params)
	if resp != nil {
		publishResponse(resp, tags)
		return
	}
	method := nip47Request.Method
	refuse := func(code, message string) { respondError(publishResponse, method, code, message) }

	// The private loop refused a missing, malformed or used request_id at I6 and
	// I7; this is the same refusal for a caller that reached here another way.
	if call == nil || !call.Meta.DeadlinePresent {
		refuse(constants.ERROR_BAD_REQUEST, "cash_transfer needs a deadline")
		return
	}
	if !nipcash.ValidRequestID(params.RequestID) {
		refuse(constants.ERROR_BAD_REQUEST, "request_id is required: 32 to 64 lowercase hex characters")
		return
	}
	if app.Kind != db.AppKindCashWallet {
		refuse(constants.ERROR_RESTRICTED, "cash_transfer requires a cash_wallet app")
		return
	}

	// Step 1 (NIP-CASH §Transferring and Splitting a Slice, Processing Algorithm):
	// a present, empty zap_target is refused first.
	if params.ZapTarget != nil && *params.ZapTarget == "" {
		refuse(constants.ERROR_BAD_REQUEST, "zap_target is present and empty")
		return
	}

	// The credential's shape: a cash secret, or an identity with its claim proof,
	// never both.
	isCashSource := params.CashSecret != ""
	if isCashSource {
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
			refuse(constants.ERROR_BAD_REQUEST, "identity_type, identity_value, and identity_event are all required")
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

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Str("new_identity_type", params.NewIdentity.IdentityType).
		Bool("cash_source", isCashSource).
		Msg("Handling cash_transfer request")

	// Locate the slice: none is NOT_FOUND; only a claimed or terminal one, or a
	// bill past its expiry, is RESTRICTED.
	claim, code, message := controller.locateSlice(app, isCashSource, params.IdentityType, params.IdentityValue)
	if code != "" {
		refuse(code, message)
		return
	}

	// The amount the claim proof binds: an omitted amount_millis is the slice's
	// full amount, never unbound. Bounded against the slice only at step 3.
	requested := uint64(claim.AmountMloki) //nolint:gosec // non-negative
	if params.AmountMillis != nil {
		requested = *params.AmountMillis
	}

	// Authorize.
	var claimSigner string
	var proofs []cashwallet.ClaimProof
	if isCashSource {
		commitment, err := nipcash.CashCommitment(params.CashSecret)
		if err != nil || subtle.ConstantTimeCompare([]byte(commitment), []byte(claim.IdentityValue)) != 1 {
			refuse(constants.ERROR_UNAUTHORIZED, "this cash_secret does not open this slice")
			return
		}
	} else {
		verdict, err := controller.verifyTransferClaimProof(app, claim, params, requested)
		if err != nil {
			var claimErr *nipcash.ClaimError
			if errors.As(err, &claimErr) && claimErr.Code == constants.ERROR_RESTRICTED {
				refuse(constants.ERROR_RESTRICTED, "the Identity Authority for this claim is no longer trusted")
				return
			}
			refuse(constants.ERROR_UNAUTHORIZED, "identity_event does not authorize this transfer: "+claimReason(err))
			return
		}
		// The item proof and the claim proof must be one signer's, or whoever
		// assembled the item could re-point a captured claim proof.
		if !nipcash.ItemSignerMatchesClaim(call.ItemSigner, verdict) {
			refuse(constants.ERROR_UNAUTHORIZED, "the item proof was not signed by the claim proof's signer")
			return
		}
		claimSigner = verdict.Signer
		proofs = []cashwallet.ClaimProof{{EventID: verdict.RecomputedID, CreatedAt: eventCreatedAt(params.IdentityEvent), Method: method}}
	}

	// The call limit, keyed by the verified claim signer or the slice's
	// commitment, never by the bill, and shared with cash_redeem and
	// cash_consolidate.
	limitKey := claimSigner
	if isCashSource {
		limitKey = claim.IdentityValue
	}
	if !controller.claimCallAllowed(limitKey) {
		refuse(constants.ERROR_RATE_LIMITED, "too many calls by this signer; retry later")
		return
	}

	// The call is authorized: its request_id is used from now on, whatever
	// happens to it (NIP-CASH §request_id). A concurrent call that marked it
	// first wins.
	if err := cashrequest.New(controller.db).MarkUsed(params.RequestID, method); err != nil {
		if errors.Is(err, cashrequest.ErrRequestReused) {
			refuse(constants.ERROR_REQUEST_REUSED, "this request_id was already used on this node")
			return
		}
		refuse(constants.ERROR_INTERNAL, "could not record this request_id, please retry")
		return
	}

	// A zap transfer's source is bound to a pubkey (NIP-CZ).
	isZap := params.ZapTarget != nil
	if isZap && claim.IdentityType != db.CashIdentityPubkey {
		refuse(constants.ERROR_RESTRICTED, "a zap transfer's source must be bound to a pubkey")
		return
	}

	// Step 2: new_identity. A cash target is the caller's own commitment: the hub
	// never generates a secret, since this reply travels over a connection every
	// co-recipient can read.
	newType := params.NewIdentity.IdentityType
	newValue := params.NewIdentity.IdentityValue
	newIA := params.NewIdentity.IAPubkey
	if isZap && newType != db.CashIdentityPubkey {
		refuse(constants.ERROR_BAD_REQUEST, "a zap transfer's new_identity must be a pubkey")
		return
	}
	if code, message := validateNewIdentity(controller.iaChecker, newType, newValue, newIA); code != "" {
		refuse(code, message)
		return
	}

	// Step 3: the amount, against the slice's committed amount (never rewritten).
	if requested == 0 {
		refuse(constants.ERROR_BAD_REQUEST, "amount_millis must be positive")
		return
	}
	if requested > uint64(claim.AmountMloki) { //nolint:gosec // non-negative
		refuse(constants.ERROR_BAD_REQUEST, fmt.Sprintf("amount_millis %d exceeds this slice's own balance of %d", requested, claim.AmountMloki))
		return
	}
	full := requested == uint64(claim.AmountMloki) //nolint:gosec // non-negative
	// A zap transfer always spins off.
	spinOff, err := isZap, error(nil)
	if !isZap {
		spinOff, err = controller.transferSpinsOff(app, claim, full, newType, newValue)
	}
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to read a Cash wallet's recipient history")
		refuse(constants.ERROR_INTERNAL, "failed to check wallet recipient history")
		return
	}

	// Step 4: a partial split leaves no dust, and a cash source's remainder takes
	// a fresh commitment so the source's secret opens nothing once it commits.
	remainder := uint64(claim.AmountMloki) - requested                       //nolint:gosec // requested <= amount
	if floor := uint64(max(claim.MinTransferMloki, 0)); !full && floor > 0 { //nolint:gosec // non-negative
		if requested < floor {
			refuse(constants.ERROR_RESTRICTED, fmt.Sprintf("amount_millis %d is below this slice's min_transfer_millis floor of %d", requested, floor))
			return
		}
		if remainder < floor {
			refuse(constants.ERROR_RESTRICTED, fmt.Sprintf("the %d remainder this split would leave behind is below this slice's min_transfer_millis floor of %d", remainder, floor))
			return
		}
	}
	partialCash := isCashSource && !full
	if (params.RemainderCommitment != "") != partialCash {
		refuse(constants.ERROR_BAD_REQUEST, "remainder_commitment is required for, and only for, a partial split of a cash slice")
		return
	}
	if partialCash && (!nipcash.ValidCommitment(params.RemainderCommitment) || params.RemainderCommitment == claim.IdentityValue) {
		refuse(constants.ERROR_BAD_REQUEST, "remainder_commitment must be a fresh 64-character lowercase hex commitment")
		return
	}

	// The record commits with the transfer (NIP-CASH §The Record): its authorizer
	// is the claim signer, or the source's commitment for a cash slice, whose
	// tokens travel in the clear.
	record := func(resultMeta any) (*db.CashRequestRecord, error) {
		kind, authorizer := "pubkey", claimSigner
		var delivery *string
		if isCashSource {
			kind, authorizer = "commitment", claim.IdentityValue
		} else {
			delivery = &claimSigner
		}
		return transferRecord(params.RequestID, app, call.Meta.Deadline, kind, authorizer, delivery, resultMeta)
	}
	source := apps.SliceRef{ClaimID: claim.ID, IdentityType: claim.IdentityType, IdentityValue: claim.IdentityValue, TransferCount: claim.TransferCount}

	if spinOff {
		newBills := 1
		if remainder > 0 {
			newBills = 2
		}
		if code, message := controller.spinOffLimitRefusal(app, newBills, true); code != "" {
			refuse(code, message)
			return
		}
		remainderType, remainderValue, remainderIA := claim.IdentityType, claim.IdentityValue, claim.IAPubkey
		if partialCash {
			remainderType, remainderValue, remainderIA = db.CashIdentityCash, params.RemainderCommitment, ""
		}
		var zap *cashwallet.ZapParams
		if isZap {
			zap = &cashwallet.ZapParams{Target: *params.ZapTarget, Sender: claim.IdentityValue}
		}
		controller.handleCashTransferSplit(ctx, method, app, claim, requested, remainder,
			newType, newValue, newIA, remainderType, remainderValue, remainderIA,
			claimSigner, source, proofs, zap, record, publishResponse, tags)
		return
	}

	// Step 5: in place. Only a full transfer reaches here, so the amount is the
	// slice's own; the identity change, the consumed proof and the record commit
	// together.
	rec, err := record(cashTransferResponse{AmountMillis: requested, IdentityType: newType, IdentityValue: newValue})
	if err != nil {
		refuse(constants.ERROR_INTERNAL, "could not record this call")
		return
	}
	if err := cashwallet.Reassign(ctx, controller.cashDeps(), cashwallet.ReassignParams{
		WalletApp:        app,
		Source:           source,
		NewIdentityType:  newType,
		NewIdentityValue: newValue,
		NewIAPubkey:      newIA,
		Proofs:           proofs,
		Record:           rec,
	}); err != nil {
		controller.respondCommitError(publishResponse, method, app, err, "failed to reassign this slice's identity")
		return
	}

	logger.Logger.Info().Uint("app_id", app.ID).Str("new_identity_type", newType).Msg("Cash wallet slice transferred")

	// identity_value is always a public identity or a commitment the caller
	// supplied, never a secret this hub generated.
	publishResponse(&models.Response{
		ResultType: method,
		Result:     cashTransferResponse{AmountMillis: requested, IdentityType: newType, IdentityValue: newValue},
	}, tags)
}

// locateSlice finds the slice a transfer acts on (NIP-CASH
// §Transferring and Splitting a Slice, step 1): the unclaimed slice registered
// to the identity, or for a cash secret the wallet's cash-mode slice. None is
// NOT_FOUND (a removed slice counts as none); a claimed or terminal one, or a
// bill past its expiry, is RESTRICTED.
func (controller *nip47Controller) locateSlice(app *db.App, isCash bool, identityType, identityValue string) (*db.CashWalletClaim, string, string) {
	q := controller.db.Where("wallet_app_id = ?", app.ID)
	if isCash {
		q = q.Where("identity_type = ?", db.CashIdentityCash)
	} else {
		q = q.Where("identity_type = ? AND identity_value = ?", identityType, identityValue)
	}
	var claims []db.CashWalletClaim
	if err := q.Order("id").Find(&claims).Error; err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to look up Cash wallet claim")
		return nil, constants.ERROR_INTERNAL, "failed to look up claim"
	}
	var live *db.CashWalletClaim
	for i := range claims {
		if claims[i].ClaimedAt == nil {
			live = &claims[i]
			break
		}
	}
	switch {
	case len(claims) == 0:
		return nil, constants.ERROR_NOT_FOUND, "no slice registered for this identity"
	case live == nil:
		message := "this slice was already redeemed or transferred"
		for _, c := range claims {
			if c.SpunOffToWalletAppID != nil {
				message += " — its value was split off into a new dedicated wallet by an earlier call; " +
					"check cash_status or that call's own response for the new connection"
				break
			}
		}
		return nil, constants.ERROR_RESTRICTED, message
	case app.ExpiresAt != nil && time.Now().After(*app.ExpiresAt):
		return nil, constants.ERROR_RESTRICTED, "this cash wallet is past its redemption deadline"
	}
	return live, "", ""
}

// verifyTransferClaimProof verifies a transfer's kind-23198 claim proof with
// nmilat (NIP-CASH §Claim Proofs): its exact tags, its binding to this wallet, m
// cash_transfer, this new_identity and this concrete amount, its freshness, that
// it is unused, and its signer (for a connection_key slice, through a live
// attestation by a trusted IA).
func (controller *nip47Controller) verifyTransferClaimProof(app *db.App, claim *db.CashWalletClaim, params *nipcash.CashTransferRequest, amount uint64) (nipcash.ClaimVerdict, error) {
	wallet := ""
	if app.WalletPubkey != nil {
		wallet = *app.WalletPubkey
	}
	store := cashrequest.New(controller.db)
	return nipcash.VerifyClaimProof(params.IdentityEvent, nipcash.ClaimExpectation{
		Binding: nipcash.ClaimBinding{
			Method:          constants.NIP47MethodCashTransfer,
			Wallet:          wallet,
			NewIdentityHash: nipcash.NewIdentityHashOf(params.NewIdentity.IdentityType, params.NewIdentity.IdentityValue, params.NewIdentity.IAPubkey),
			AmountMillis:    &amount,
		},
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

// claimReason is a claim-proof refusal's reason, without nmilat's prefix.
func claimReason(err error) string {
	var claimErr *nipcash.ClaimError
	if errors.As(err, &claimErr) {
		return claimErr.Reason
	}
	return "it does not verify"
}

// eventCreatedAt is a claim proof's created_at, the retention anchor of its
// consumed id; 0 when it does not parse (it verified, so it does).
func eventCreatedAt(raw string) int64 {
	var ev struct {
		CreatedAt int64 `json:"created_at"`
	}
	_ = json.Unmarshal([]byte(raw), &ev)
	return ev.CreatedAt
}

// transferSpinsOff decides a transfer's outcome (NIP-CASH §Which outcome a
// request produces): a partial split always spins off. A full transfer to a
// pubkey or connection_key is in place, unless that identity already holds
// another unclaimed slice on this wallet. A slice of its whose payout is still in
// flight counts as held: a failed payout releases it, and the wallet would then
// hold two live slices for one identity. A full transfer to cash is in place
// only on a wallet that has, and has always had, exactly one recipient.
func (controller *nip47Controller) transferSpinsOff(app *db.App, claim *db.CashWalletClaim, full bool, newType, newValue string) (bool, error) {
	if !full {
		return true, nil
	}
	if newType != db.CashIdentityCash {
		var held int64
		err := controller.db.Model(&db.CashWalletClaim{}).
			Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND id <> ?",
				app.ID, newType, newValue, claim.ID).
			Where("claimed_at IS NULL OR (payment_hash <> '' AND preimage = '')").
			Count(&held).Error
		return held > 0, err
	}
	// Cash: a cash redemption carries its raw secret in the request body, so a
	// cash slice may stay on a connection only no one else ever held. "Always had"
	// is the lifetime set: live claims, archived (removed) slices, and in-place
	// handoffs, which TransferCount counts. The pairing secret derives from the app
	// id and cannot be rotated, so every former holder still reads this connection.
	all, err := controller.appsService.ListClaimsForWallet(app.ID)
	if err != nil {
		return false, err
	}
	if len(all) != 1 || claim.TransferCount > 0 {
		return true, nil
	}
	removed, err := controller.appsService.HasArchivedSliceForWallet(app.ID)
	if err != nil {
		return false, err
	}
	return removed, nil
}

// spinOffLimitRefusal bounds spin-offs (NIP-CASH §Security Considerations for
// Cash-Mode Slices): the live bills under the Cash Hub (QUOTA_EXCEEDED), then,
// when lineage is set, per bill lineage per hour (RATE_LIMITED). The cap is read
// first, so a call it refuses spends none of the lineage's allowance. "" when
// the spin-off may proceed.
func (controller *nip47Controller) spinOffLimitRefusal(app *db.App, newBills int, lineage bool) (string, string) {
	env := controller.cfg.GetEnv()
	if limit := env.CashMaxLiveBillsPerHub; limit > 0 && app.ParentAppID != nil {
		var live int64
		if err := controller.db.Model(&db.App{}).
			Where("parent_app_id = ? AND kind = ?", *app.ParentAppID, db.AppKindCashWallet).Count(&live).Error; err != nil {
			logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to count a Cash Hub's live bills")
			return constants.ERROR_INTERNAL, "failed to check this hub's live bills"
		}
		if live+int64(newBills) > int64(limit) {
			return constants.ERROR_QUOTA_EXCEEDED, fmt.Sprintf("this Cash Hub holds its maximum of %d live bills", limit)
		}
	}
	if lineage && controller.lineageLimiter != nil &&
		!controller.lineageLimiter.Allow(strconv.FormatUint(uint64(cashwallet.LineageRoot(app)), 10), env.CashLineageSpinOffsPerHour) {
		return constants.ERROR_RATE_LIMITED, "this bill's lineage has spun off too many bills this hour; retry later"
	}
	return "", ""
}

// handleCashTransferSplit spins a slice off into one or two new dedicated bills
// (NIP-CASH §Spinning a Slice Off Into a Dedicated Wallet): the carved bill for
// new_identity and, for a partial split, the remainder bill. The source claim,
// the new bills, their funding, the consumed proof and the record are one
// commit (cashwallet.SplitInTwo): a failure leaves the slice claimable and the
// proof unused. Each token is nested-encrypted to deliverTo, the claim signer, or
// returned in the clear for a cash source (single-recipient by construction).
func (controller *nip47Controller) handleCashTransferSplit(ctx context.Context, method string, app *db.App,
	claim *db.CashWalletClaim, amount, remainder uint64,
	newType, newValue, newIA, remainderType, remainderValue, remainderIA string,
	deliverTo string, source apps.SliceRef, proofs []cashwallet.ClaimProof, zap *cashwallet.ZapParams,
	record func(resultMeta any) (*db.CashRequestRecord, error), publishResponse publishFunc, tags nostr.Tags) {
	if app.ParentAppID == nil {
		logger.Logger.Error().Uint("app_id", app.ID).Msg("cash_wallet has no parent hub; cannot split off a slice")
		respondError(publishResponse, method, constants.ERROR_INTERNAL, "failed to split off slice")
		return
	}
	hubApp := controller.appsService.GetAppById(*app.ParentAppID)
	if hubApp == nil {
		logger.Logger.Error().Uint("app_id", app.ID).Uint("hub_app_id", *app.ParentAppID).Msg("cash_wallet's parent hub no longer exists")
		respondError(publishResponse, method, constants.ERROR_INTERNAL, "failed to split off slice")
		return
	}

	meta := cashTransferResponse{AmountMillis: amount, IdentityType: newType, IdentityValue: newValue}
	if remainder > 0 {
		meta.RemainingAmountMillis = &remainder
	}
	rec, err := record(meta)
	if err != nil {
		respondError(publishResponse, method, constants.ERROR_INTERNAL, "could not record this call")
		return
	}
	// Both new bills inherit the source slice's expiry, floor and fee: a split
	// relocates an entitlement, never grants, shortens or lengthens one.
	result, err := cashwallet.SplitInTwo(ctx, controller.cashDeps(), cashwallet.SplitInTwoParams{
		HubApp:                 hubApp,
		SourceWalletApp:        app,
		Source:                 source,
		CarvedIdentityType:     newType,
		CarvedIdentityValue:    newValue,
		CarvedIAPubkey:         newIA,
		CarvedAmountMloki:      amount,
		RemainderIdentityType:  remainderType,
		RemainderIdentityValue: remainderValue,
		RemainderIAPubkey:      remainderIA,
		RemainderAmountMloki:   remainder,
		MinTransferMloki:       claim.MinTransferMloki,
		RedeemFeePpm:           claim.RedeemFeePpm,
		RedeemFeeBaseMloki:     claim.RedeemFeeBaseMloki,
		Ordered:                claim.Ordered,
		ExpiresAt:              app.ExpiresAt,
		Proofs:                 proofs,
		Record:                 rec,
		Zap:                    zap,
	})
	if err != nil {
		controller.respondCommitError(publishResponse, method, app, err, "failed to split off slice")
		return
	}

	// The commit is done, so the source slice is terminal on every path from here,
	// including a connection that cannot be delivered below. A drained source is
	// auto-deleted after the response (NIP-CASH §Lifecycle and Deletion).
	defer controller.maybeAutoDeleteDrainedCashWallet(app)

	deliver := func(w *cashwallet.SplitResult) (pubkey, token string, ok bool) {
		if w.WalletApp.WalletPubkey != nil {
			pubkey = *w.WalletApp.WalletPubkey
		}
		if deliverTo == "" {
			return pubkey, w.CashToken, true
		}
		privKey, keyErr := controller.keys.GetAppWalletKey(w.WalletApp.ID)
		if keyErr != nil {
			logger.Logger.Error().Err(keyErr).Uint("new_wallet_id", w.WalletApp.ID).
				Msg("Failed to derive split-off wallet key; the bill committed but could not be delivered")
			return "", "", false
		}
		enc, encErr := encryptForRecipient(deliverTo, privKey, w.CashToken)
		if encErr != nil {
			logger.Logger.Error().Err(encErr).Uint("new_wallet_id", w.WalletApp.ID).
				Msg("Failed to encrypt split-off wallet token; the bill committed but could not be delivered")
			return "", "", false
		}
		return pubkey, enc, true
	}

	carvedPubkey, carvedToken, ok := deliver(result.Carved)
	if !ok {
		respondError(publishResponse, method, constants.ERROR_INTERNAL,
			"slice was split off but its connection could not be delivered; contact the wallet operator")
		return
	}
	resp := cashTransferResponse{
		AmountMillis:    amount,
		IdentityType:    newType,
		IdentityValue:   newValue,
		NewWalletPubkey: carvedPubkey,
		NewWalletToken:  carvedToken,
	}
	if z := result.Carved.Zap; z != nil {
		issued, expires := z.IssuedAt, z.ExpiresAt
		resp.IssuedAt, resp.ExpiresAt = &issued, &expires
	}
	if result.Remainder != nil {
		remPubkey, remToken, ok := deliver(result.Remainder)
		if !ok {
			respondError(publishResponse, method, constants.ERROR_INTERNAL,
				"slice was split off but the remainder connection could not be delivered; contact the wallet operator")
			return
		}
		resp.RemainingAmountMillis = &remainder
		resp.RemainderWalletPubkey = remPubkey
		resp.RemainderWalletToken = remToken
	}

	logger.Logger.Info().
		Uint("app_id", app.ID).
		Uint("carved_wallet_id", result.Carved.WalletApp.ID).
		Uint64("carved_amount_mloki", amount).
		Uint64("remainder_amount_mloki", remainder).
		Msg("Cash wallet slice split off into fresh dedicated wallet(s)")

	publishResponse(&models.Response{ResultType: method, Result: resp}, tags)
}

// transferRecord is a transfer's record: its target is this bill, its result_meta
// the reply's non-secret fields other than the new bills, which the record's bill
// rows carry.
func transferRecord(requestID string, bill *db.App, deadline int64, authorizerKind, authorizer string, delivery *string, resultMeta any) (*db.CashRequestRecord, error) {
	raw, err := json.Marshal(resultMeta)
	if err != nil {
		return nil, err
	}
	target := ""
	if bill.WalletPubkey != nil {
		target = *bill.WalletPubkey
	}
	return &db.CashRequestRecord{
		RequestID:          requestID,
		Method:             constants.NIP47MethodCashTransfer,
		HubAppID:           derefUint(bill.ParentAppID),
		TargetWalletPubkey: target,
		TargetConnPubkey:   bill.AppPubkey,
		AuthorizerKind:     authorizerKind,
		Authorizer:         authorizer,
		Deadline:           deadline,
		DeliveryPubkey:     delivery,
		ResultMeta:         string(raw),
	}, nil
}

func derefUint(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}

// cashDeps are the services a cash commit needs.
func (controller *nip47Controller) cashDeps() cashwallet.Deps {
	return cashwallet.Deps{
		AppsService:         controller.appsService,
		TransactionsService: controller.transactionsService,
		LNClient:            controller.lnClient,
		Keys:                controller.keys,
		DB:                  controller.db,
		RelayURLs:           controller.cfg.GetRelayUrls(),
		IAChecker:           controller.iaChecker,
		Hooks:               controller.cashHooks,
	}
}

// respondCommitError answers a failed cash commit. Nothing was committed: the
// outcomes NIP-CASH names get their code and a fixed message; anything else is
// logged and answered with fallback, never echoing an internal error.
func (controller *nip47Controller) respondCommitError(publishResponse publishFunc, method string, app *db.App, err error, fallback string) {
	code := mapCashWalletErrorCode(err)
	if errors.Is(err, cashrequest.ErrDeadlinePassed) || errors.Is(err, apps.ErrSliceClaimed) || errors.Is(err, cashwallet.ErrProofUsed) {
		respondError(publishResponse, method, code, commitErrorMessage(err))
		return
	}
	logger.Logger.Error().Err(err).Uint("app_id", app.ID).Str("method", method).Msg("Cash commit failed; nothing was committed")
	respondError(publishResponse, method, code, fallback)
}

// maybeAutoDeleteDrainedCashWallet deletes app if it has no unclaimed slices
// left AND its real ledger balance is exactly zero — a multi-recipient
// wallet with other still-unclaimed slices must never be deleted just
// because one recipient's slice fully split away, since the wallet's real
// balance still legitimately backs those other slices. Best-effort: logged,
// never fails the caller's request, and the periodic expiry-sweep ticker
// (service.runCashCleanup) remains the fallback. The policy itself lives in
// cashwallet.AutoDeleteDrained, shared with the redeem payouts
// that settle only after cash_redeem has answered.
func (controller *nip47Controller) maybeAutoDeleteDrainedCashWallet(app *db.App) {
	deleted, err := cashwallet.AutoDeleteDrained(controller.db, controller.eventPublisher, app.ID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).
			Msg("Failed to archive and auto-delete fully-drained Cash wallet")
		return
	}
	if !deleted {
		// Expected: another slice is still unclaimed, or the balance is not
		// zero. Either way the bill legitimately stays alive.
		logger.Logger.Debug().Uint("app_id", app.ID).
			Msg("Cash wallet not fully drained; leaving it alive")
		return
	}
	logger.Logger.Info().Uint("app_id", app.ID).
		Msg("Archived and auto-deleted fully-drained Cash wallet")
}
