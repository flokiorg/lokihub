package cashwithdraw

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokiorg/go-flokicoin/chaincfg"
	"github.com/flokiorg/go-flokicoin/chainutil"
	"github.com/flokiorg/go-flokicoin/chainutil/bech32"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
)

// U-23.5: the quote's tags equal the answer's fields, it verifies against the
// node key, sent_by = min(now + 3600, send_before), holds_until = tip + 2, and
// nothing is written.
func TestU_23_5_QuoteFieldsAndNoWrites(t *testing.T) {
	w := newWorld(t)
	bill, hs := w.mintPubkey(t, 21_000_000)
	addr := address(t, 0, 20)
	before := w.snapshot(t)

	res, err := w.quote(t, bill, hs[0], map[string]any{"address": addr})
	require.NoError(t, err)
	xonly, _ := w.node.XOnly(context.TODO())
	q, err := nipcash.VerifyQuoteAnswer(*res, nipcash.QuoteExpectation{HubXOnly: xonly, Wallet: *bill.WalletPubkey,
		IdentityValue: hs[0].pub, Address: addr, AmountMillis: 21_000_000})
	require.NoError(t, err)
	assert.Equal(t, w.clock().Unix()+3600, q.SentBy)
	assert.Equal(t, int64(1002), q.HoldsUntil)
	assert.Equal(t, before, w.snapshot(t), "a quote changes no row")

	sendBefore := w.clock().Unix() + 1200
	res, err = w.quote(t, bill, hs[0], map[string]any{"address": addr, "send_before": sendBefore})
	require.NoError(t, err)
	assert.Equal(t, sendBefore, res.SentBy)
}

// U-23.4 / K-WFEE: the fee is WithdrawFee of the live network fee for the
// address's script type and the hub's terms, floored at the slice's redeem fee;
// the network fee follows the node's estimate and is never a constant.
func TestU_23_4_QuoteFee(t *testing.T) {
	w := newWorld(t)
	bill, hs := w.mintPubkey(t, 21_000_000)
	for _, tc := range []struct{ v, n uint8 }{{0, 20}, {0, 32}, {1, 32}} {
		addr := address(t, int(tc.v), int(tc.n))
		res, err := w.quote(t, bill, hs[0], map[string]any{"address": addr})
		require.NoError(t, err)
		st := nipcash.ScriptType{Version: tc.v, ProgramLen: tc.n}
		r := RatesFrom(w.payer.est)
		network := NetworkFee(r, st)
		hub, fee, payout, err := nipcash.WithdrawFee(21_000_000, network, termsFeeBase, termsFeePpm, 0)
		require.NoError(t, err)
		assert.Equal(t, network, res.NetworkFeeMillis)
		assert.Equal(t, hub, res.HubFeeMillis)
		assert.Equal(t, fee, res.FeeMillis)
		assert.Equal(t, payout, res.PayoutMillis)
		assert.Zero(t, res.PayoutMillis%1000)
	}
	// 31 vs 43 vbytes of output.
	r := RatesFrom(w.payer.est)
	assert.EqualValues(t, 1000*r.SatPerVB*(31+110), NetworkFee(r, nipcash.ScriptType{Version: 0, ProgramLen: 20}))
	assert.EqualValues(t, 1000*r.SatPerVB*(43+110), NetworkFee(r, nipcash.ScriptType{Version: 1, ProgramLen: 32}))
	// Floored at min relay + 1 unit/vB.
	low := RatesFrom(lnclient.FeeEstimate{SatPerKw: 1, MinRelaySatPerKw: 250})
	assert.Equal(t, int64(2), low.SatPerVB)

	// The estimator changes, the quote changes (after the cache expires).
	addr := address(t, 0, 20)
	first, err := w.quote(t, bill, hs[0], map[string]any{"address": addr})
	require.NoError(t, err)
	w.payer.mu.Lock()
	w.payer.est.SatPerKw = 12_500
	w.payer.mu.Unlock()
	w.advance(61 * time.Second)
	second, err := w.quote(t, bill, hs[0], map[string]any{"address": addr})
	require.NoError(t, err)
	assert.Greater(t, second.NetworkFeeMillis, first.NetworkFeeMillis)

	// The slice's own redeem fee is the floor.
	var claim db.CashWalletClaim
	require.NoError(t, w.svc.DB.Where("wallet_app_id = ?", bill.ID).First(&claim).Error)
	require.NoError(t, w.svc.DB.Model(&claim).Update("redeem_fee_ppm", 500_000).Error)
	res, err := w.quote(t, bill, hs[0], map[string]any{"address": addr})
	require.NoError(t, err)
	assert.Equal(t, uint64(10_500_000), res.FeeMillis)
}

// U-23.1 / U-23.7 / K-ADDR: the refusal table in order, with its details.
func TestU_23_7_QuoteRefusals(t *testing.T) {
	w := newWorld(t)
	bill, hs := w.mintPubkey(t, 21_000_000, 500_000, 2_000_000_000)
	h := hs[0]
	good := address(t, 0, 20)
	testnet, err := chainutil.NewAddressWitnessPubKeyHash(make([]byte, 20), &chaincfg.TestNet3Params)
	require.NoError(t, err)
	p2pkh, err := chainutil.NewAddressPubKeyHash(make([]byte, 20), &chaincfg.RegressionNetParams)
	require.NoError(t, err)
	conv, err := bech32.ConvertBits(make([]byte, 20), 8, 5, true)
	require.NoError(t, err)
	unpaid, err := bech32.EncodeM(chaincfg.RegressionNetParams.Bech32HRPSegwit, append([]byte{1}, conv...))
	require.NoError(t, err)

	cases := []struct {
		name   string
		h      holder
		params map[string]any
		code   string
		detail nipcash.Detail
	}{
		{"no slice", newHolder(), map[string]any{"address": "nonsense"}, constants.ERROR_NOT_FOUND, ""},
		{"malformed", h, map[string]any{"address": "nonsense"}, constants.ERROR_BAD_REQUEST, ""},
		{"uppercase", h, map[string]any{"address": strings.ToUpper(good)}, constants.ERROR_BAD_REQUEST, ""},
		{"address not a string", h, map[string]any{"address": 12}, constants.ERROR_BAD_REQUEST, ""},
		{"caller fee", h, map[string]any{"address": good, "fee_millis": 1}, constants.ERROR_BAD_REQUEST, ""},
		{"cash secret", h, map[string]any{"address": good, "cash_secret": strings.Repeat("a", 64)}, constants.ERROR_BAD_REQUEST, ""},
		{"ck fields on a pubkey bill", h, map[string]any{"address": good, "identity_value": h.pub}, constants.ERROR_BAD_REQUEST, ""},
		{"network", h, map[string]any{"address": testnet.EncodeAddress()}, constants.ERROR_RESTRICTED, nipcash.DetailNetwork},
		{"base58", h, map[string]any{"address": p2pkh.EncodeAddress()}, constants.ERROR_RESTRICTED, nipcash.DetailScriptType},
		{"unpaid script type", h, map[string]any{"address": unpaid}, constants.ERROR_RESTRICTED, nipcash.DetailScriptType},
		{"below min", hs[1], map[string]any{"address": good}, constants.ERROR_RESTRICTED, nipcash.DetailBelowMin},
		{"above max", hs[2], map[string]any{"address": good}, constants.ERROR_QUOTA_EXCEEDED, ""},
		{"too slow", h, map[string]any{"address": good, "send_before": w.clock().Unix() + 599}, constants.ERROR_RESTRICTED, nipcash.DetailTooSlow},
		// Adjacent pairs: the earlier row wins.
		{"network before script type", h, map[string]any{"address": testnet.EncodeAddress(), "send_before": 1}, constants.ERROR_RESTRICTED, nipcash.DetailNetwork},
		{"above max before too slow", hs[2], map[string]any{"address": good, "send_before": 1}, constants.ERROR_QUOTA_EXCEEDED, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.quote(t, bill, tc.h, tc.params)
			requireCode(t, err, tc.code, tc.detail)
		})
	}

	// send_before at now + 600 is reachable.
	_, err = w.quote(t, bill, h, map[string]any{"address": good, "send_before": w.clock().Unix() + 600})
	require.NoError(t, err)

	// A payout below dust is below_min: the slice's fee eats it.
	var claim db.CashWalletClaim
	require.NoError(t, w.svc.DB.Where("identity_value = ?", h.pub).First(&claim).Error)
	require.NoError(t, w.svc.DB.Model(&claim).Update("redeem_fee_ppm", 999_990).Error)
	_, err = w.quote(t, bill, h, map[string]any{"address": good})
	requireCode(t, err, constants.ERROR_RESTRICTED, nipcash.DetailBelowMin)

	// A claimed slice is RESTRICTED without detail, before the max row.
	now := time.Now()
	require.NoError(t, w.svc.DB.Model(&db.CashWalletClaim{}).Where("identity_value = ?", hs[2].pub).Update("claimed_at", &now).Error)
	_, err = w.quote(t, bill, hs[2], map[string]any{"address": good})
	requireCode(t, err, constants.ERROR_RESTRICTED, "")
}

// Dust per script type follows go-flokicoin's policy.
func TestDustPerScriptType(t *testing.T) {
	r := RatesFrom(lnclient.FeeEstimate{SatPerKw: 253, MinRelaySatPerKw: 253})
	d20 := DustMillis(r, nipcash.ScriptType{Version: 0, ProgramLen: 20})
	d32 := DustMillis(r, nipcash.ScriptType{Version: 1, ProgramLen: 32})
	assert.Positive(t, d20)
	assert.GreaterOrEqual(t, d32, d20)
	assert.Zero(t, d20%1000)
}

// U-23.5: a node that cannot sign answers INTERNAL "quote unavailable".
func TestU_23_5_QuoteSignerFailure(t *testing.T) {
	w := newWorld(t)
	bill, hs := w.mintPubkey(t, 21_000_000)
	w.node.signErr = errors.New("signer down")
	_, err := w.quote(t, bill, hs[0], map[string]any{"address": address(t, 0, 20)})
	requireCode(t, err, constants.ERROR_INTERNAL, "")
}

// U-23.6: a cash-mode bill is quoted on the bill proof alone.
func TestU_23_6_CashModeQuote(t *testing.T) {
	w := newWorld(t)
	bill, _ := w.mintCash(t, 21_000_000)
	res, err := w.s.Quote(context.TODO(), bill, Caller{CashMode: true, LimitKey: "envelope:x"}, raw(t, map[string]any{"address": address(t, 1, 32)}))
	require.NoError(t, err)
	q, err := nipcash.ParseWithdrawQuote(res.Quote)
	require.NoError(t, err)
	assert.Empty(t, q.IAPubkey)
}

// 120 quotes an hour per signer, then RATE_LIMITED; never before NOT_FOUND.
func TestQuoteRateLimit(t *testing.T) {
	w := newWorld(t)
	bill, hs := w.mintPubkey(t, 21_000_000)
	addr := address(t, 0, 20)
	for range quotesPerHour {
		_, err := w.quote(t, bill, hs[0], map[string]any{"address": addr})
		require.NoError(t, err)
	}
	_, err := w.quote(t, bill, hs[0], map[string]any{"address": addr})
	requireCode(t, err, constants.ERROR_RATE_LIMITED, "")
	w.advance(time.Hour + time.Second)
	_, err = w.quote(t, bill, hs[0], map[string]any{"address": addr})
	require.NoError(t, err)
}

// U-23.3: terms are refused, never clamped; enabling needs the switch and the
// capability; FinalDepth is at least 6; a terms change leaves an issued quote
// unchanged.
func TestU_23_3_Terms(t *testing.T) {
	w := newWorld(t)
	neg, one, two := int64(-1), int64(1), int64(2)
	for _, in := range []TermsInput{
		{FeePpm: 1_000_001}, {FeePpm: -1}, {FeeBaseMillis: -1}, {MinMillis: &neg}, {MinMillis: &two, MaxMillis: &one},
	} {
		_, err := w.s.PutTerms(w.hub.ID, in)
		assert.ErrorIs(t, err, constants.ErrInvalidParams, "%+v", in)
	}
	v, err := w.s.GetTerms(w.hub.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(termsFeePpm), v.FeePpm, "a refused change is not applied")
	assert.Equal(t, 6, v.FinalDepth)
	assert.Equal(t, ScriptTypes, v.ScriptTypes)
	assert.True(t, v.Offered)

	bill, hs := w.mintPubkey(t, 21_000_000)
	addr := address(t, 0, 20)
	q := w.mustQuote(t, bill, hs[0], addr)
	_, err = w.s.PutTerms(w.hub.ID, TermsInput{Enabled: true, FeePpm: 900_000})
	require.NoError(t, err)
	p, err := w.withdraw(t, bill, hs[0], request(t, bill, nipcash.BySigning(hs[0].priv), q, newRID(), nil))
	require.NoError(t, err)
	assert.Equal(t, q.PayoutMillis, p.PayoutMillis, "an accepted quote keeps its payout")

	require.NoError(t, w.svc.Cfg.SetUpdate("CashWithdrawEnabled", "false", ""))
	_, err = w.s.PutTerms(w.hub.ID, TermsInput{Enabled: true})
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
	assert.False(t, w.s.Offered(w.hub.ID))
	assert.Nil(t, w.s.StatusTerms(w.hub.ID))

	require.NoError(t, w.svc.Cfg.SetUpdate("CashWithdrawEnabled", "true", ""))
	w.node.payer = nil
	_, err = w.s.PutTerms(w.hub.ID, TermsInput{Enabled: true})
	assert.ErrorIs(t, err, constants.ErrInvalidParams, "no on-chain capability")
	assert.False(t, w.s.Offered(w.hub.ID))
	assert.Error(t, w.s.SetNodeEnabled(true))
}

// U-24.18 in WP-23's terms: without an engine (or before its reconcile) nothing
// is offered.
func TestOfferedNeedsEngine(t *testing.T) {
	w := newWorld(t)
	assert.True(t, w.s.Offered(w.hub.ID))
	w.funds.notReady = true
	assert.False(t, w.s.Offered(w.hub.ID))
	w.s.Funds = nil
	assert.False(t, w.s.Offered(w.hub.ID))
}
