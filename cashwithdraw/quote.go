package cashwithdraw

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// Refusal is a withdrawal refusal that is not one of nmilat's table rows:
// a code, an optional detail and a message for people. *nipcash.WithdrawError
// is the other refusal the service returns; anything else is INTERNAL.
type Refusal struct {
	Code    string
	Detail  nipcash.Detail
	Message string
}

func (r *Refusal) Error() string { return "cashwithdraw: " + r.Code + ": " + r.Message }

func refuse(code, message string) error { return &Refusal{Code: code, Message: message} }

// quotesPerHour bounds the quotes one item-proof signer (a bearer: one
// envelope) may ask for an hour.
const quotesPerHour = 120

// Caller is who sends a bill item: the verified item-proof signer ("" for a
// bearer item), whether the bill is cash-mode by the Hub's records, and the key
// the per-caller limits count under.
type Caller struct {
	ItemSigner string
	CashMode   bool
	LimitKey   string
}

// quoteParams are a quote's params, decoded by type.
type quoteParams struct {
	Address          string
	SendBefore       *int64
	IdentityValue    string
	IAPubkey         string
	AttestationEvent string
	hasCK            bool
}

// callerFeeField reports a field by which a caller tries to set a fee or a
// payout: the Hub computes those (NIP-CASH §Security Considerations).
func callerFeeField(k string) bool {
	for _, p := range []string{"fee", "network_fee", "hub_fee", "payout"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// decodeQuoteParams decodes a quote's params by type. A wrong type, a
// cash_secret or a caller fee field is a shape error, answered BAD_REQUEST at
// the malformed-address row; any other field is ignored.
func decodeQuoteParams(raw json.RawMessage) (quoteParams, string) {
	var p quoteParams
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return p, "params are not an object"
	}
	str := func(k string, dst *string) string {
		v, ok := fields[k]
		if !ok {
			return ""
		}
		if err := json.Unmarshal(v, dst); err != nil {
			return k + " is not a string"
		}
		return ""
	}
	for k := range fields {
		if k == "cash_secret" {
			return p, "a quote carries no cash_secret"
		}
		if callerFeeField(k) {
			return p, k + " is the Hub's to compute and may not be sent"
		}
	}
	if msg := str("address", &p.Address); msg != "" {
		return p, msg
	}
	if v, ok := fields["send_before"]; ok {
		var n int64
		dec := json.NewDecoder(bytes.NewReader(v))
		dec.UseNumber()
		var num json.Number
		if err := dec.Decode(&num); err != nil {
			return p, "send_before is not an integer"
		}
		n, err := num.Int64()
		if err != nil || n < 0 {
			return p, "send_before is not an integer"
		}
		p.SendBefore = &n
	}
	for _, f := range []struct {
		k   string
		dst *string
	}{{"identity_value", &p.IdentityValue}, {"ia_pubkey", &p.IAPubkey}, {"attestation_event", &p.AttestationEvent}} {
		if _, ok := fields[f.k]; ok {
			p.hasCK = true
			if msg := str(f.k, f.dst); msg != "" {
				return p, msg
			}
		}
	}
	return p, ""
}

// locatedSlice is the slice an item names, as the quote and the call see it.
type locatedSlice struct {
	claim *db.CashWalletClaim
	// trusted is false for a connection_key slice whose IA is no longer
	// trusted.
	trusted bool
}

// iaTrusted reports an IA on the allowlist.
func (s *Service) iaTrusted(ia string) bool {
	if s.IA == nil {
		return false
	}
	ok, err := s.IA.IsTrusted(ia)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to check Identity Authority trust")
	}
	return err == nil && ok
}

// locateForQuote finds the slice a quote names (Q1): a cash-mode bill's cash
// slice; a connection_key slice named by identity_value and ia_pubkey whose
// attestation verifies for the item-proof signer; else the pubkey slice of the
// item-proof signer. nil when none (NOT_FOUND). shape is a BAD_REQUEST reason
// for connection_key fields on a bill without such a slice.
func (s *Service) locateForQuote(bill *db.App, c Caller, p quoteParams, now int64) (*locatedSlice, string, error) {
	var claims []db.CashWalletClaim
	if err := s.DB.Where("wallet_app_id = ?", bill.ID).Order("id").Find(&claims).Error; err != nil {
		return nil, "", err
	}
	hasCK := false
	for _, cl := range claims {
		if cl.IdentityType == db.CashIdentityConnectionKey {
			hasCK = true
		}
	}
	if p.hasCK && !hasCK {
		return nil, "identity_value, ia_pubkey and attestation_event are only for a connection_key slice", nil
	}
	for i := range claims {
		cl := &claims[i]
		switch {
		case c.CashMode && c.ItemSigner == "":
			if cl.IdentityType == db.CashIdentityCash {
				return &locatedSlice{claim: cl, trusted: true}, "", nil
			}
		case p.hasCK:
			if cl.IdentityType != db.CashIdentityConnectionKey || cl.IdentityValue != p.IdentityValue || cl.IAPubkey != p.IAPubkey {
				continue
			}
			trusted, err := nipcash.VerifySliceAttestation(p.AttestationEvent, nipcash.AttestationExpectation{
				SliceValue: cl.IdentityValue, SliceIA: cl.IAPubkey, Signer: c.ItemSigner, IATrusted: s.iaTrusted,
			}, now)
			if err != nil {
				return nil, "", nil
			}
			return &locatedSlice{claim: cl, trusted: trusted}, "", nil
		default:
			if cl.IdentityType == db.CashIdentityPubkey && c.ItemSigner != "" && cl.IdentityValue == c.ItemSigner {
				return &locatedSlice{claim: cl, trusted: true}, "", nil
			}
		}
	}
	return nil, "", nil
}

func billExpiry(bill *db.App) *int64 {
	if bill.ExpiresAt == nil {
		return nil
	}
	v := bill.ExpiresAt.Unix()
	return &v
}

// claimable: unclaimed, not terminal, live, and (connection_key) trusted.
func claimable(sl *locatedSlice, bill *db.App, now int64) bool {
	return sl.claim.ClaimedAt == nil && nipcash.SliceLive(now, billExpiry(bill)) && sl.trusted
}

// redeemFee is the slice's own redeem fee, the floor of a withdraw fee.
func redeemFee(cl *db.CashWalletClaim) (uint64, error) {
	fee, _, err := nipcash.RedeemFee(uint64(max(cl.AmountMloki, 0)), uint64(max(cl.RedeemFeeBaseMloki, 0)), uint64(max(cl.RedeemFeePpm, 0))) //nolint:gosec // non-negative
	return fee, err
}

// allowQuote counts one quote under key; false once over quotesPerHour.
func (s *Service) allowQuote(key string, now time.Time) bool {
	s.quoteMu.Lock()
	defer s.quoteMu.Unlock()
	if s.quotes == nil {
		s.quotes = map[string][]int64{}
	}
	cut := now.Add(-time.Hour).Unix()
	kept := s.quotes[key][:0]
	for _, t := range s.quotes[key] {
		if t > cut {
			kept = append(kept, t)
		}
	}
	if len(kept) >= quotesPerHour {
		s.quotes[key] = kept
		return false
	}
	s.quotes[key] = append(kept, now.Unix())
	return true
}

// Quote answers cash_withdraw_quote for bill (NIP-CASH §Quoting a Withdrawal),
// after the transport's checks. It writes nothing: the quote is signed by the
// node identity and is checked at the call by its signature alone. A refusal is
// a *nipcash.WithdrawError (the spec's table, in order) or a *Refusal.
func (s *Service) Quote(ctx context.Context, bill *db.App, c Caller, params json.RawMessage) (*nipcash.CashWithdrawQuoteResult, error) {
	now := s.now()
	p, shape := decodeQuoteParams(params)

	sl, ckShape, err := s.locateForQuote(bill, c, p, now.Unix())
	if err != nil {
		return nil, err
	}
	if shape == "" {
		shape = ckShape
	}
	hubID := uint(0)
	if bill.ParentAppID != nil {
		hubID = *bill.ParentAppID
	}
	if sl == nil {
		if shape != "" && ckShape != "" {
			// connection_key fields on a bill holding none name no slice: a
			// malformed request rather than an unknown one.
			return nil, &nipcash.WithdrawError{Code: nipcash.ErrCodeBadRequest, Row: 2, Reason: shape}
		}
		return nil, &nipcash.WithdrawError{Code: nipcash.ErrCodeNotFound, Row: 1, Reason: "no slice"}
	}
	if !s.allowQuote("quote:"+c.LimitKey, now) {
		return nil, refuse(nipcash.ErrCodeRateLimited, "too many quotes; retry later")
	}
	if shape != "" {
		return nil, &nipcash.WithdrawError{Code: nipcash.ErrCodeBadRequest, Row: 2, Reason: shape}
	}

	n, err := s.node()
	if err != nil {
		return nil, err
	}
	hrp, err := n.HRP(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.terms(s.DB, hubID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, refuse(nipcash.ErrCodeNotImplemented, "this hub does not offer on-chain withdrawals")
	}
	floor, err := redeemFee(sl.claim)
	if err != nil {
		return nil, err
	}
	amount := uint64(max(sl.claim.AmountMloki, 0)) //nolint:gosec // non-negative
	var hubFee, payout, network uint64
	payoutFor := func(st nipcash.ScriptType) (uint64, uint64, error) {
		r, err := s.Rates(ctx)
		if err != nil {
			return 0, 0, err
		}
		network = NetworkFee(r, st)
		hubFee, _, payout, err = nipcash.WithdrawFee(amount, network, uint64(max(t.FeeBaseMillis, 0)), uint64(max(t.FeePpm, 0)), floor) //nolint:gosec // non-negative
		if err != nil {
			return 0, 0, err
		}
		return payout, DustMillis(r, st), nil
	}
	var minM, maxM *uint64
	if t.MinMillis != nil {
		v := uint64(*t.MinMillis) //nolint:gosec // non-negative
		minM = &v
	}
	if t.MaxMillis != nil {
		v := uint64(*t.MaxMillis) //nolint:gosec // non-negative
		maxM = &v
	}
	env := s.env()
	if err := nipcash.CheckQuoteRequest(nipcash.QuoteCheck{
		Found: true, Address: p.Address, NetworkHRP: hrp, Pays: pays(),
		Claimable: claimable(sl, bill, now.Unix()), AmountMillis: amount, MinMillis: minM, MaxMillis: maxM,
		PayoutFor: payoutFor, SendBefore: p.SendBefore, EarliestSentBy: now.Unix() + env.MinLeadSecs(),
	}); err != nil {
		return nil, err
	}
	tip, err := n.Tip(ctx)
	if err != nil {
		return nil, err
	}
	sentBy := now.Unix() + env.SentBySecs()
	if p.SendBefore != nil {
		sentBy = min(sentBy, *p.SendBefore)
	}
	fields := nipcash.QuoteFields{
		Wallet: deref(bill.WalletPubkey), IdentityValue: sl.claim.IdentityValue, Address: p.Address,
		AmountMillis: amount, NetworkFeeMillis: network, HubFeeMillis: hubFee, PayoutMillis: payout,
		SentBy: sentBy, HoldsUntil: tip + env.QuoteBlocks(),
	}
	if sl.claim.IdentityType == db.CashIdentityConnectionKey {
		fields.IAPubkey = sl.claim.IAPubkey
	}
	ev, err := nipcash.NewWithdrawQuoteEvent(fields, now.Unix(), signer(ctx, n))
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", bill.ID).Msg("Could not sign a withdraw quote")
		return nil, refuse(nipcash.ErrCodeInternal, "quote unavailable")
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	return &nipcash.CashWithdrawQuoteResult{
		Quote: string(raw), Address: p.Address, AmountMillis: amount, NetworkFeeMillis: network,
		HubFeeMillis: hubFee, FeeMillis: amount - payout, PayoutMillis: payout, SentBy: sentBy, HoldsUntil: fields.HoldsUntil,
	}, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
