package cashwithdraw

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipcash"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/ledger"
	"github.com/flokiorg/lokihub/logger"
)

// nsWithdrawLockKey is the advisory lock every write that changes the set of
// held payouts or set-aside coins takes, after the hub and bill locks: the
// unconfirmed bound and the set-aside serialise on it ("cashwdr").
const nsWithdrawLockKey = 0x63617368_776472

// lockTx takes the per-app advisory locks (ascending, as cashwallet's commit
// does) and then nsWithdraw. sqlite's IMMEDIATE transaction already holds its
// one write lock.
func lockTx(tx *gorm.DB, appIDs ...uint) error {
	if tx.Name() != "postgres" {
		return nil
	}
	ids := append([]uint(nil), appIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var last uint
	for i, id := range ids {
		if id == 0 || (i > 0 && id == last) {
			continue
		}
		last = id
		if err := tx.Exec("SELECT pg_advisory_xact_lock($1)", int64(id)).Error; err != nil { //nolint:gosec // small app ids
			return fmt.Errorf("lock app %d: %w", id, err)
		}
	}
	return tx.Exec("SELECT pg_advisory_xact_lock($1)", int64(nsWithdrawLockKey)).Error
}

// CallMeta is what the transport knows of a call: its deadline (the envelope's
// not_after) and the relay it arrived on.
type CallMeta struct {
	Deadline     int64
	ArrivalRelay string
}

// MarkUsedFunc marks a request_id used (cashrequest.Store.MarkUsed); a test may
// replace it.
type MarkUsedFunc func(id, method string) error

// QuoteForSlice is the step-3 row-1 check: q verifies under hubXOnly and is for
// this bill and slice — d is wallet, i is the slice's identity value and IA
// pubkey (empty unless connection_key), and amount_millis is the slice's
// committed amount now. The amount matters: an in-place transfer can give the
// same identity another slice of another amount on the same wallet, and a quote
// for the old amount must not pay out against it (nmilat review H3, CT-WD-27).
// A failure is RESTRICTED with detail quote_invalid.
//
// TODO(nmilat): switch to nipcash.QuoteForSlice (nmilat 87e1099, CT-WD-27) once
// lokihub is pinned past parity-1; this mirrors its semantics.
func QuoteForSlice(q *nipcash.WithdrawQuote, hubXOnly, wallet, identityValue, iaPubkey string, amountMillis uint64) error {
	if err := nipcash.VerifyWithdrawQuote(q, hubXOnly); err != nil {
		return err
	}
	if q.Wallet != wallet || q.IdentityValue != identityValue || q.IAPubkey != iaPubkey || q.AmountMillis != amountMillis {
		return &nipcash.WithdrawError{Code: nipcash.ErrCodeRestricted, Detail: nipcash.DetailQuoteInvalid, Row: 1,
			Reason: "the quote is not for this bill and slice"}
	}
	return nil
}

// recomputedID is an event's NIP-01 id, recomputed (never the id it claims).
func recomputedID(ev *nip01.Event) (string, error) {
	id, err := ev.HashID()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(id), nil
}

// sliceOf locates the slice a cash_withdraw names exactly as cash_redeem step
// 1: NOT_FOUND without one, RESTRICTED when it is claimed, terminal or past its
// expiry.
func (s *Service) sliceOf(bill *db.App, isCash bool, identityType, identityValue string, now int64) (*db.CashWalletClaim, error) {
	q := s.DB.Where("wallet_app_id = ?", bill.ID)
	if isCash {
		q = q.Where("identity_type = ?", db.CashIdentityCash)
	} else {
		q = q.Where("identity_type = ? AND identity_value = ?", identityType, identityValue)
	}
	var claims []db.CashWalletClaim
	if err := q.Order("id").Find(&claims).Error; err != nil {
		return nil, err
	}
	if len(claims) == 0 {
		return nil, refuse(nipcash.ErrCodeNotFound, "no slice registered for this identity")
	}
	for i := range claims {
		if claims[i].ClaimedAt == nil {
			if !nipcash.SliceLive(now, billExpiry(bill)) {
				return nil, refuse(nipcash.ErrCodeRestricted, "this cash wallet is past its redemption deadline")
			}
			return &claims[i], nil
		}
	}
	return nil, refuse(nipcash.ErrCodeRestricted, "this slice was already redeemed or transferred")
}

// callShape is the credential's shape, as cash_redeem's: a cash secret or an
// identity with its claim proof, never both.
func callShape(req *nipcash.CashWithdrawRequest) error {
	if req.CashSecret != "" {
		if req.IdentityType != "" || req.IdentityValue != "" || req.IdentityEvent != "" || req.AttestationEvent != "" {
			return refuse(nipcash.ErrCodeBadRequest, "cash_secret is mutually exclusive with the identity fields")
		}
		if !nipcash.ValidCashSecret(req.CashSecret) {
			return refuse(nipcash.ErrCodeBadRequest, "cash_secret must be 64 lowercase hex characters")
		}
		return nil
	}
	switch {
	case req.IdentityType == "" || req.IdentityValue == "" || req.IdentityEvent == "":
		return refuse(nipcash.ErrCodeBadRequest, "identity_type, identity_value and identity_event are required")
	case req.IdentityType != db.CashIdentityPubkey && req.IdentityType != db.CashIdentityConnectionKey:
		return refuse(nipcash.ErrCodeBadRequest, "identity_type must be pubkey or connection_key")
	case req.IdentityType == db.CashIdentityConnectionKey && req.AttestationEvent == "":
		return refuse(nipcash.ErrCodeBadRequest, "attestation_event is required for a connection_key slice")
	}
	return nil
}

// billCommitment is a cash-mode bill's slice commitment, "" for any other.
func (s *Service) billCommitment(bill *db.App) (string, bool, error) {
	var claims []db.CashWalletClaim
	if err := s.DB.Where("wallet_app_id = ? AND identity_type = ?", bill.ID, db.CashIdentityCash).Find(&claims).Error; err != nil {
		return "", false, err
	}
	if len(claims) == 0 {
		return "", false, nil
	}
	return claims[0].IdentityValue, true, nil
}

// prefetched are the facts read before the commit opens: node RPCs never run
// with a write transaction held.
type prefetched struct {
	tip       int64
	hubXOnly  string
	script    []byte
	version   uint8
	ownScript bool
	unfinal   bool
	funds     FundsSnapshot
	final     int
}

// Withdraw serves cash_withdraw for bill (NIP-CASH §Redeeming On-chain,
// §Processing Algorithm), after the transport's checks: the BAD_REQUEST list
// before step 1, locate, authorize and mark the request_id used, check the
// payout, then one commit. A refusal is a *nipcash.WithdrawError or a
// *Refusal; anything else is INTERNAL with nothing committed.
func (s *Service) Withdraw(ctx context.Context, bill *db.App, c Caller, meta CallMeta, params json.RawMessage, markUsed MarkUsedFunc) (*nipcash.Payout, error) {
	now := s.now()
	commitment, cashMode, err := s.billCommitment(bill)
	if err != nil {
		return nil, err
	}

	// W0: before step 1. The request_id is not marked.
	req, err := nipcash.ParseCashWithdrawRequest(params, nipcash.WithdrawSlice{CashMode: cashMode, Commitment: commitment})
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(params, &fields)
	for k := range fields {
		if callerFeeField(k) {
			return nil, refuse(nipcash.ErrCodeBadRequest, k+" is the Hub's to compute and may not be sent")
		}
	}
	if err := callShape(req); err != nil {
		return nil, err
	}
	quote, err := nipcash.ParseWithdrawQuote(req.Quote)
	if err != nil {
		return nil, err
	}
	quoteID, err := recomputedID(quote.Event)
	if err != nil {
		return nil, refuse(nipcash.ErrCodeBadRequest, "malformed quote")
	}

	// W1: locate.
	isCash := req.CashSecret != ""
	claim, err := s.sliceOf(bill, isCash, req.IdentityType, req.IdentityValue, now.Unix())
	if err != nil {
		return nil, err
	}
	wallet := deref(bill.WalletPubkey)

	// W2: authorize, then mark the request_id used.
	var authorizerKind, authorizer string
	var delivery *string
	var proofID string
	var proofCreatedAt int64
	if isCash {
		got, cErr := nipcash.CashCommitment(req.CashSecret)
		if cErr != nil || subtle.ConstantTimeCompare([]byte(got), []byte(claim.IdentityValue)) != 1 {
			return nil, refuse(nipcash.ErrCodeUnauthorized, "this cash_secret does not open this slice")
		}
		authorizerKind, authorizer = "commitment", claim.IdentityValue
	} else {
		store := cashrequest.Store{DB: s.DB, Now: s.now}
		verdict, vErr := nipcash.VerifyClaimProof(req.IdentityEvent, nipcash.ClaimExpectation{
			Binding:     nipcash.ClaimBinding{Method: nipcash.MethodCashWithdraw, Wallet: wallet, QuoteID: quoteID},
			SliceType:   claim.IdentityType,
			SliceValue:  claim.IdentityValue,
			SliceIA:     claim.IAPubkey,
			Attestation: req.AttestationEvent,
			IATrusted:   s.iaTrusted,
			Consumed: func(id string) bool {
				used, err := store.IsProofConsumed(id)
				return err == nil && used
			},
		}, now)
		if vErr != nil {
			var ce *nipcash.ClaimError
			if errors.As(vErr, &ce) && ce.Code == nipcash.ErrCodeRestricted {
				return nil, refuse(nipcash.ErrCodeRestricted, "the Identity Authority for this claim is no longer trusted")
			}
			return nil, refuse(nipcash.ErrCodeUnauthorized, "identity_event does not authorize this withdrawal")
		}
		if !nipcash.ItemSignerMatchesClaim(c.ItemSigner, verdict) {
			return nil, refuse(nipcash.ErrCodeUnauthorized, "the item proof was not signed by the claim proof's signer")
		}
		authorizerKind, authorizer = "pubkey", verdict.Signer
		d := verdict.Signer
		delivery = &d
		proofID = verdict.RecomputedID
		proofCreatedAt = int64(eventCreatedAt(req.IdentityEvent))
	}
	if markUsed == nil {
		markUsed = cashrequest.Store{DB: s.DB, Now: s.now}.MarkUsed
	}
	if err := markUsed(req.RequestID, nipcash.MethodCashWithdraw); err != nil {
		if errors.Is(err, cashrequest.ErrRequestReused) {
			return nil, refuse(nipcash.ErrCodeRequestReused, "this request_id was already used on this node")
		}
		return nil, err
	}
	// From here the id stays used whatever follows.

	hubID := uint(0)
	if bill.ParentAppID != nil {
		hubID = *bill.ParentAppID
	}
	amount := uint64(max(claim.AmountMloki, 0)) //nolint:gosec // non-negative

	// W3: prefetch outside any database transaction.
	pf, err := s.prefetch(ctx, hubID, claim, quote)
	if err != nil {
		return nil, err
	}

	// W4: the payout checks, in order, own_address last. W5: address, amounts,
	// sent_by and holds_until come from the quote only.
	quoteOK := QuoteForSlice(quote, pf.hubXOnly, wallet, claim.IdentityValue, iaOf(claim), amount) == nil && pf.script != nil
	atBound, canSetAside, err := s.fundsChecks(s.DB, pf.funds, quote)
	if err != nil {
		return nil, err
	}
	if err := nipcash.CheckWithdrawPayout(nipcash.PayoutCheck{
		QuoteOK: quoteOK, QuoteValid: nipcash.QuoteValidAt(quote.HoldsUntil, pf.tip),
		AtUnconfirmedBound: atBound, CoinsSetAside: canSetAside,
		OrderedUnfinal: pf.unfinal, RefuseUnfinal: true, OwnAddress: pf.ownScript,
	}); err != nil {
		return nil, err
	}

	// W6: the commit. The tip is read again just before it opens.
	tip, err := s.tipNow(ctx)
	if err != nil {
		return nil, err
	}
	w, err := s.commitWithdraw(ctx, commitInput{
		bill: bill, hubID: hubID, claim: claim, req: req, quote: quote, quoteID: quoteID, meta: meta, pf: pf,
		tip: tip, amount: amount, authorizerKind: authorizerKind, authorizer: authorizer, delivery: delivery,
		proofID: proofID, proofCreatedAt: proofCreatedAt,
	})
	if err != nil {
		return nil, err
	}

	// W7: after COMMIT.
	if s.Funds != nil {
		s.Funds.Signal()
	}
	if s.Events != nil {
		if _, err := cashwallet.AutoDeleteDrained(s.DB, s.Events, bill.ID); err != nil {
			logger.Logger.Error().Err(err).Uint("app_id", bill.ID).Msg("Failed to auto-delete a drained bill after cash_withdraw")
		}
	}
	view, _, err := s.PayoutView(s.DB, w)
	if err != nil {
		return nil, err
	}
	return view, nil
}

func iaOf(c *db.CashWalletClaim) string {
	if c.IdentityType == db.CashIdentityConnectionKey {
		return c.IAPubkey
	}
	return ""
}

func eventCreatedAt(raw string) uint64 {
	var ev struct {
		CreatedAt uint64 `json:"created_at"`
	}
	_ = json.Unmarshal([]byte(raw), &ev)
	return ev.CreatedAt
}

func (s *Service) tipNow(ctx context.Context) (int64, error) {
	n, err := s.node()
	if err != nil {
		return 0, err
	}
	return n.Tip(ctx)
}

func (s *Service) prefetch(ctx context.Context, hubID uint, claim *db.CashWalletClaim, q *nipcash.WithdrawQuote) (prefetched, error) {
	var pf prefetched
	n, err := s.node()
	if err != nil {
		return pf, err
	}
	if pf.tip, err = n.Tip(ctx); err != nil {
		return pf, err
	}
	if pf.hubXOnly, err = n.XOnly(ctx); err != nil {
		return pf, err
	}
	hrp, err := n.HRP(ctx)
	if err != nil {
		return pf, err
	}
	if v, prog, aerr := nipcash.DecodeWitnessAddress(q.Address, hrp); aerr == nil {
		pf.version, pf.script = v, nipcash.WitnessScript(v, prog)
		if pf.ownScript, err = s.IsOwnScript(ctx, pf.script); err != nil {
			return pf, err
		}
	}
	if pf.final, err = s.FinalDepth(s.DB, hubID); err != nil {
		return pf, err
	}
	if pf.unfinal, err = s.orders().OrderedUnfinal(s.DB, claim, pf.final); err != nil {
		return pf, err
	}
	if s.Funds != nil {
		if pf.funds, err = s.Funds.Snapshot(ctx); err != nil {
			return pf, err
		}
	}
	return pf, nil
}

// fundsChecks are step 3's rows 3 and 4: the bound and the set-aside.
func (s *Service) fundsChecks(tx *gorm.DB, snap FundsSnapshot, q *nipcash.WithdrawQuote) (atBound, canSetAside bool, err error) {
	if s.Funds == nil {
		return false, false, nil
	}
	if atBound, err = s.Funds.AtUnconfirmedBoundTx(tx, snap, q.PayoutMillis); err != nil {
		return false, false, err
	}
	if canSetAside, err = s.Funds.CanSetAsideTx(tx, q.PayoutMillis, q.NetworkFeeMillis); err != nil {
		return false, false, err
	}
	return atBound, canSetAside, nil
}

type commitInput struct {
	bill           *db.App
	hubID          uint
	claim          *db.CashWalletClaim
	req            *nipcash.CashWithdrawRequest
	quote          *nipcash.WithdrawQuote
	quoteID        string
	meta           CallMeta
	pf             prefetched
	tip            int64
	amount         uint64
	authorizerKind string
	authorizer     string
	delivery       *string
	proofID        string
	proofCreatedAt int64
}

// errDeadline is a commit past the call's deadline.
var errDeadline = &Refusal{Code: nipcash.ErrCodeBadRequest, Message: "the request reached its commit after its deadline"}

// commitWithdraw is step 4 in one transaction: re-check under the locks, end
// the slice, debit the bill by exactly its amount, hold it on the payout, set
// coins aside, consume the claim proof, sign the receipt, write the record and
// last re-check the deadline. Any failure before COMMIT leaves only the mark.
func (s *Service) commitWithdraw(ctx context.Context, in commitInput) (*db.CashWithdrawal, error) {
	var out *db.CashWithdrawal
	n, err := s.node()
	if err != nil {
		return nil, err
	}
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockTx(tx, in.hubID, in.bill.ID); err != nil {
			return err
		}
		if err := s.before(StageAfterLock); err != nil {
			return err
		}
		now := s.now()
		if !nipcash.CommitAllowed(in.meta.Deadline, now.Unix()) {
			return errDeadline
		}
		if !nipcash.QuoteValidAt(in.quote.HoldsUntil, in.tip) {
			return &nipcash.WithdrawError{Code: nipcash.ErrCodeRestricted, Detail: nipcash.DetailQuoteExpired, Row: 2, Reason: "quote expired"}
		}
		if !nipcash.SliceLive(now.Unix(), billExpiry(in.bill)) {
			return refuse(nipcash.ErrCodeRestricted, "this cash wallet is past its redemption deadline")
		}
		if in.claim.IdentityType == db.CashIdentityConnectionKey && !iaTrustedTx(tx, in.claim.IAPubkey) {
			return refuse(nipcash.ErrCodeRestricted, "the Identity Authority for this claim is no longer trusted")
		}
		atBound, canSetAside, err := s.fundsChecks(tx, in.pf.funds, in.quote)
		if err != nil {
			return err
		}
		if atBound {
			return &nipcash.WithdrawError{Code: nipcash.ErrCodeRateLimited, Row: 3, Reason: "unconfirmed payouts at their bound"}
		}
		if !canSetAside {
			return &nipcash.WithdrawError{Code: nipcash.ErrCodeInsufficientBalance, Row: 4, Reason: "no coins to set aside"}
		}
		if err := s.before(StageAfterRecheck); err != nil {
			return err
		}

		ref := apps.SliceRef{ClaimID: in.claim.ID, IdentityType: in.claim.IdentityType, IdentityValue: in.claim.IdentityValue, TransferCount: in.claim.TransferCount}
		if err := apps.ClaimCashSliceTx(tx, ref, nil, db.CashSliceStatusRedeemed, now); err != nil {
			if errors.Is(err, apps.ErrSliceClaimed) {
				return refuse(nipcash.ErrCodeRestricted, "this slice was already claimed by another call")
			}
			return err
		}
		if err := s.before(StageAfterSourceClaim); err != nil {
			return err
		}
		if err := ledger.ExternalDebitTx(tx, in.bill.ID, in.amount, nipcash.MethodCashWithdraw, in.req.RequestID); err != nil {
			return err
		}
		if err := s.before(StageAfterDebit); err != nil {
			return err
		}

		ordered, unfinalIDs, err := s.orders().Ordered(tx, in.claim)
		if err != nil {
			return err
		}
		q := in.quote
		w := &db.CashWithdrawal{
			RequestID: in.req.RequestID, HubAppID: in.hubID, BillAppID: in.bill.ID,
			BillWalletPubkey: deref(in.bill.WalletPubkey), ClaimID: in.claim.ID,
			IdentityType: in.claim.IdentityType, IdentityValue: in.claim.IdentityValue, IAPubkey: iaOf(in.claim),
			DeliveryPubkey: in.delivery, SliceExpiresAt: billExpiry(in.bill), Ordered: ordered,
			RedeemFeeBaseMillis: in.claim.RedeemFeeBaseMloki, RedeemFeePpm: in.claim.RedeemFeePpm,
			MinTransferMillis: in.claim.MinTransferMloki, UnfinalPaymentIDs: unfinalIDs,
			QuoteEvent: in.req.Quote, QuoteID: in.quoteID, Address: q.Address, WitnessVersion: int16(in.pf.version),
			OutputScript: hex.EncodeToString(in.pf.script),
			AmountMillis: int64(q.AmountMillis), NetworkFeeMillis: int64(q.NetworkFeeMillis), //nolint:gosec // bounded
			HubFeeMillis: int64(q.HubFeeMillis), FeeMillis: int64(q.FeeMillis()), PayoutMillis: int64(q.PayoutMillis), //nolint:gosec // bounded
			SentBy: q.SentBy, HoldsUntil: q.HoldsUntil, FinalDepth: in.pf.final,
			Notify: in.req.Notify, ArrivalRelay: in.meta.ArrivalRelay,
			State: db.CashWithdrawQueued, HeldMillis: int64(in.amount), //nolint:gosec // bounded
			CommittedAt: now.Unix(), StateAt: now.Unix(),
		}
		if in.req.RemintCommitment != "" {
			rc := in.req.RemintCommitment
			w.RemintCommitment = &rc
		}
		if err := tx.Create(w).Error; err != nil {
			return err
		}
		if err := s.before(StageAfterHold); err != nil {
			return err
		}
		if s.Funds == nil {
			return errors.New("no payout engine")
		}
		if err := s.Funds.SetAsideTx(tx, w.ID, q.PayoutMillis, q.NetworkFeeMillis); err != nil {
			return err
		}
		if err := s.before(StageAfterSetAside); err != nil {
			return err
		}
		if in.proofID != "" {
			store := cashrequest.Store{DB: tx, Now: s.now}
			if err := store.ConsumeProofTx(tx, in.proofID, in.proofCreatedAt, nipcash.MethodCashWithdraw); err != nil {
				if errors.Is(err, cashrequest.ErrProofConsumed) {
					return refuse(nipcash.ErrCodeUnauthorized, "this claim proof was already used")
				}
				return err
			}
		}
		if err := s.before(StageAfterProof); err != nil {
			return err
		}
		receipt, err := nipcash.NewWithdrawReceiptEvent(nipcash.ReceiptFields{
			Wallet: w.BillWalletPubkey, QuoteID: in.quoteID, RequestID: in.req.RequestID, Address: q.Address,
			PayoutMillis: q.PayoutMillis, SentBy: q.SentBy,
		}, now.Unix(), signer(ctx, n))
		if err != nil {
			logger.Logger.Error().Err(err).Uint("app_id", in.bill.ID).Msg("Could not sign a withdraw receipt; nothing was committed")
			return refuse(nipcash.ErrCodeInternal, "receipt unavailable")
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		w.ReceiptEvent = string(raw)
		if err := s.before(StageAfterReceiptSign); err != nil {
			return err
		}
		if err := appendEventTx(tx, w, now.Unix(), nil); err != nil {
			return err
		}

		rec := &db.CashRequestRecord{
			RequestID: in.req.RequestID, Method: nipcash.MethodCashWithdraw, WithdrawalID: &w.ID, HubAppID: in.hubID,
			TargetWalletPubkey: w.BillWalletPubkey, TargetConnPubkey: in.bill.AppPubkey,
			AuthorizerKind: in.authorizerKind, Authorizer: in.authorizer, Deadline: in.meta.Deadline,
			DeliveryPubkey: in.delivery, ResultMeta: "{}",
		}
		store := cashrequest.Store{DB: tx, Now: s.now}
		if err := store.CommitTx(tx, rec, nil); err != nil {
			if errors.Is(err, cashrequest.ErrDeadlinePassed) {
				return errDeadline
			}
			return err
		}
		w.RecordID = rec.ID
		if err := tx.Model(&db.CashWithdrawal{}).Where("id = ?", w.ID).
			Updates(map[string]interface{}{"record_id": rec.ID, "receipt_event": w.ReceiptEvent}).Error; err != nil {
			return err
		}
		// The withdraw record's retention is this package's (retention.go).
		if err := tx.Model(&db.CashRequestRecord{}).Where("id = ?", rec.ID).Update("drop_after", nil).Error; err != nil {
			return err
		}
		if err := s.before(StageAfterRecord); err != nil {
			return err
		}
		if err := s.before(StageBeforeCommit); err != nil {
			return err
		}
		if !nipcash.CommitAllowed(in.meta.Deadline, s.now().Unix()) {
			return errDeadline
		}
		out = w
		return nil
	})
	if err != nil {
		return nil, err
	}
	logger.Logger.Info().Uint("withdrawal_id", out.ID).Uint("hub_app_id", out.HubAppID).
		Int64("payout_millis", out.PayoutMillis).Str("address_prefix", prefix8(out.Address)).Msg("cash_withdraw committed")
	return out, nil
}

// prefix8 is the most of an address or a notify value a log line carries.
func prefix8(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// appendEventTx appends an archive fact for w's current state.
func appendEventTx(tx *gorm.DB, w *db.CashWithdrawal, at int64, minerFeeMillis *int64) error {
	ev := db.CashWithdrawEvent{WithdrawalID: w.ID, At: at, State: w.State, Txid: w.Txid, Vout: w.Vout, MinerFeeMillis: minerFeeMillis}
	if w.Txid != nil {
		d := w.Depth
		ev.Depth = &d
	}
	return tx.Create(&ev).Error
}

// noticeDue is when a state notice committed at at is published: at plus a
// uniform delay in [600, 1200) s from crypto/rand, never aligned to blocks.
func noticeDue(at time.Time) (int64, error) {
	d, err := nipcash.WithdrawNoticeDelay(rand.Reader)
	if err != nil {
		return 0, err
	}
	return at.Add(d).Unix(), nil
}

// iaTrustedTx is the IA allowlist read inside the commit's own transaction (a
// second connection would wait on sqlite's one writer).
func iaTrustedTx(tx *gorm.DB, ia string) bool {
	var n int64
	err := tx.Table("identity_authorities").Where("pubkey = ?", strings.ToLower(strings.TrimSpace(ia))).Count(&n).Error
	return err == nil && n > 0
}
