package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/niplsp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/lsps/lsps1"
	"github.com/flokiorg/lokihub/lsps/lsps2"
	"github.com/flokiorg/lokihub/lsps/persist"
)

// liveRow is announcedRow as a live LSPS2 menu row.
func liveRow() lsps2.OpeningFeeParams {
	return lsps2.OpeningFeeParams{MinFeeMloki: 2000000, Proportional: 4000, MinLifetime: 4320,
		MaxClientToSelfDelay: 2016, MinPaymentSizeMloki: 10000000, MaxPaymentSizeMloki: 4000000000,
		ValidUntil: time.Now().Add(time.Hour).UTC(), Promise: "promise"}
}

// fakeLSP answers the LSPS2 messages the manager sends: get_info with menus
// in turn (the last one again once they run out), buy with a channel, and it
// records the fee params of every buy.
type fakeLSP struct {
	mu     sync.Mutex
	menus  [][]lsps2.OpeningFeeParams
	gets   int
	bought []lsps2.OpeningFeeParams
}

func (f *fakeLSP) run(ctx context.Context, _ *testing.T, m *LiquidityManager, ln *mockLNClientJIT) {
	go m.processInternalEvents(ctx)
	go func() {
		for ctx.Err() == nil {
			ln.mu.Lock()
			msgs := ln.sentMsgs
			ln.sentMsgs = nil
			ln.mu.Unlock()
			for _, msg := range msgs {
				var req struct {
					ID     string          `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				_ = json.Unmarshal(msg.Data, &req)
				var result any
				f.mu.Lock()
				switch req.Method {
				case lsps2.MethodGetInfo:
					result = lsps2.GetInfoResponse{OpeningFeeParamsMenu: f.menus[min(f.gets, len(f.menus)-1)]}
					f.gets++
				case lsps2.MethodBuy:
					var p lsps2.BuyRequest
					_ = json.Unmarshal(req.Params, &p)
					f.bought = append(f.bought, p.OpeningFeeParams)
					result = lsps2.BuyResponse{JitChannelSCID: "800000x1x1", LSPNodeID: gateLSP, LSPCLTVExpiryDelta: 144}
				}
				f.mu.Unlock()
				raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
				_ = m.lsps2Client.HandleMessage(msg.PeerPubkey, raw)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

func (f *fakeLSP) buys() []lsps2.OpeningFeeParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]lsps2.OpeningFeeParams(nil), f.bought...)
}

// passingCheck is a live check that found the announced terms: the bait LSP
// answers it honestly and changes its menu at buy time.
func passingCheck(m *LiquidityManager) {
	m.SetLiveChecker(func(context.Context, string) (*LiveCheck, error) { return &LiveCheck{Usable: true}, nil })
}

// NIP-LSP §Client Behavior, Live check; §Security, Bait terms: the automatic
// JIT purchase buys only from a menu fetched at buy time and compared with
// the announcement as not worse for the buyer, so an LSP that answers the live
// check with the announced terms cannot then sell at a higher fee. Each fee
// row field is tried one step worse (refused, nothing bought), the same and
// one step better (bought, at exactly that row).
func TestAutomaticJITBuysOnlyTermsComparedWithTheAnnouncement(t *testing.T) {
	steps := map[string]struct {
		worse, better func(p *lsps2.OpeningFeeParams)
	}{
		"min_fee":                  {func(p *lsps2.OpeningFeeParams) { p.MinFeeMloki++ }, func(p *lsps2.OpeningFeeParams) { p.MinFeeMloki-- }},
		"proportional":             {func(p *lsps2.OpeningFeeParams) { p.Proportional++ }, func(p *lsps2.OpeningFeeParams) { p.Proportional-- }},
		"min_lifetime":             {func(p *lsps2.OpeningFeeParams) { p.MinLifetime-- }, func(p *lsps2.OpeningFeeParams) { p.MinLifetime++ }},
		"max_client_to_self_delay": {func(p *lsps2.OpeningFeeParams) { p.MaxClientToSelfDelay-- }, func(p *lsps2.OpeningFeeParams) { p.MaxClientToSelfDelay++ }},
		"min_payment_size":         {func(p *lsps2.OpeningFeeParams) { p.MinPaymentSizeMloki++ }, func(p *lsps2.OpeningFeeParams) { p.MinPaymentSizeMloki-- }},
		"max_payment_size":         {func(p *lsps2.OpeningFeeParams) { p.MaxPaymentSizeMloki-- }, func(p *lsps2.OpeningFeeParams) { p.MaxPaymentSizeMloki++ }},
	}
	const amount = 100_000_000
	for field, step := range steps {
		for _, c := range []struct {
			name   string
			edit   func(p *lsps2.OpeningFeeParams)
			refuse bool
		}{{"worse", step.worse, true}, {"same", func(*lsps2.OpeningFeeParams) {}, false}, {"better", step.better, false}} {
			t.Run(field+"/"+c.name, func(t *testing.T) {
				m, ln, _ := gateManager(t, nil)
				passingCheck(m)
				menu := liveRow()
				c.edit(&menu)
				lsp := &fakeLSP{menus: [][]lsps2.OpeningFeeParams{{menu}}}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				lsp.run(ctx, t, m, ln)

				hints, err := m.EnsureInboundLiquidity(ctx, amount)
				if c.refuse {
					assert.ErrorIs(t, err, ErrLSPNotUsable)
					assert.ErrorIs(t, err, ErrTermsWorse)
					assert.Contains(t, err.Error(), "lsps2_fee[0]."+field)
					assert.Empty(t, lsp.buys(), "nothing bought at worse terms")
					return
				}
				require.NoError(t, err)
				require.Len(t, lsp.buys(), 1)
				assert.Equal(t, menu.Promise, lsp.buys()[0].Promise)
				assert.Equal(t, menu.MinFeeMloki, lsp.buys()[0].MinFeeMloki)
				fee, _ := jitFee(amount, menu)
				assert.Equal(t, fee, hints.FeeMloki)
			})
		}
	}
}

// A buy refused for stale params is retried on a menu fetched and compared
// again: worse terms at the retry are refused like at the first try, and the
// same terms are bought.
func TestJITRetryComparesTheFreshMenuAgain(t *testing.T) {
	for _, c := range []struct {
		name   string
		retry  func(p *lsps2.OpeningFeeParams)
		refuse bool
	}{{"worse at the retry", func(p *lsps2.OpeningFeeParams) { p.MinFeeMloki *= 10 }, true}, {"same at the retry", func(*lsps2.OpeningFeeParams) {}, false}} {
		t.Run(c.name, func(t *testing.T) {
			m, ln, _ := gateManager(t, nil)
			passingCheck(m)
			first, retry := liveRow(), liveRow()
			first.Promise, retry.Promise = "stale", "fresh"
			c.retry(&retry)
			lsp := &fakeLSP{menus: [][]lsps2.OpeningFeeParams{{first}, {retry}}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// The first buy is refused as stale; answer it with code 201.
			go m.processInternalEvents(ctx)
			go func() {
				for ctx.Err() == nil {
					ln.mu.Lock()
					msgs := ln.sentMsgs
					ln.sentMsgs = nil
					ln.mu.Unlock()
					for _, msg := range msgs {
						var req struct {
							ID     string          `json:"id"`
							Method string          `json:"method"`
							Params json.RawMessage `json:"params"`
						}
						_ = json.Unmarshal(msg.Data, &req)
						var raw []byte
						lsp.mu.Lock()
						switch req.Method {
						case lsps2.MethodGetInfo:
							raw, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID,
								"result": lsps2.GetInfoResponse{OpeningFeeParamsMenu: lsp.menus[min(lsp.gets, 1)]}})
							lsp.gets++
						case lsps2.MethodBuy:
							var p lsps2.BuyRequest
							_ = json.Unmarshal(req.Params, &p)
							lsp.bought = append(lsp.bought, p.OpeningFeeParams)
							if p.OpeningFeeParams.Promise == "stale" {
								raw = []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"error":{"code":201,"message":"invalid_opening_fee_params"}}`, req.ID))
							} else {
								raw, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID,
									"result": lsps2.BuyResponse{JitChannelSCID: "800000x1x1", LSPNodeID: gateLSP}})
							}
						}
						lsp.mu.Unlock()
						_ = m.lsps2Client.HandleMessage(msg.PeerPubkey, raw)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}()

			_, err := m.EnsureInboundLiquidity(ctx, 100_000_000)
			buys := lsp.buys()
			if c.refuse {
				assert.ErrorIs(t, err, ErrTermsWorse)
				require.Len(t, buys, 1, "only the stale buy, at the announced terms")
				assert.Equal(t, "stale", buys[0].Promise)
				return
			}
			require.NoError(t, err)
			require.Len(t, buys, 2)
			assert.Equal(t, "fresh", buys[1].Promise)
		})
	}
}

// An LSP whose announcement names no LSPS2 fee row has no terms to compare a
// JIT purchase with: nothing is bought from it, automatically or manually.
func TestNoJITPurchaseWithoutAnnouncedFeeTerms(t *testing.T) {
	m, ln, _ := gateManager(t, func(l *persist.LSP) { announce(l) })
	passingCheck(m)
	lsp := &fakeLSP{menus: [][]lsps2.OpeningFeeParams{{liveRow()}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lsp.run(ctx, t, m, ln)
	_, err := m.EnsureInboundLiquidity(ctx, 100_000_000)
	assert.ErrorIs(t, err, ErrLSPNotUsable)
	p := liveRow()
	_, err = m.BuyLiquidity(ctx, gateLSP, 100_000_000, &p, nil)
	assert.ErrorIs(t, err, ErrLSPNotUsable)
	assert.Empty(t, lsp.buys())
}

// A manual JIT purchase buys exactly the fee row the user was shown. One worse
// than announced is refused with the terms compared until the user accepts
// every worse value at its live value; an acceptance of other values does not
// count. The announced row itself, and a better one, need no acceptance.
func TestManualJITAsksAgainForWorseTerms(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, ln, _ := gateManager(t, nil)
	passingCheck(m)
	lsp := &fakeLSP{menus: [][]lsps2.OpeningFeeParams{{liveRow()}}}
	lsp.run(ctx, t, m, ln)
	const amount = 100_000_000

	worse := liveRow()
	worse.MinFeeMloki, worse.Proportional = 2000001, 4001
	_, err := m.BuyLiquidity(ctx, gateLSP, amount, &worse, nil)
	var tw *TermsWorseError
	require.ErrorAs(t, err, &tw)
	assert.True(t, tw.Terms.Worse)
	assert.Contains(t, tw.Terms.Fields, TermField{Field: "lsps2_fee[0].min_fee", Announced: "2000000", Live: "2000001", Verdict: "worse"})
	assert.Empty(t, lsp.buys())

	partly := []AcceptedTerm{{Field: "lsps2_fee[0].min_fee", Live: "2000001"}}
	_, err = m.BuyLiquidity(ctx, gateLSP, amount, &worse, partly)
	assert.ErrorIs(t, err, ErrTermsWorse, "proportional not accepted")
	stale := append(partly, AcceptedTerm{Field: "lsps2_fee[0].proportional", Live: "4000"})
	_, err = m.BuyLiquidity(ctx, gateLSP, amount, &worse, stale)
	assert.ErrorIs(t, err, ErrTermsWorse, "accepted at another value")
	assert.Empty(t, lsp.buys())

	all := append(partly, AcceptedTerm{Field: "lsps2_fee[0].proportional", Live: "4001"})
	_, err = m.BuyLiquidity(ctx, gateLSP, amount, &worse, all)
	require.NoError(t, err)
	require.Len(t, lsp.buys(), 1)
	assert.Equal(t, uint64(2000001), lsp.buys()[0].MinFeeMloki)

	for _, p := range []lsps2.OpeningFeeParams{liveRow(), func() lsps2.OpeningFeeParams { p := liveRow(); p.MinFeeMloki--; return p }()} {
		_, err = m.BuyLiquidity(ctx, gateLSP, amount, &p, nil)
		require.NoError(t, err)
	}
	assert.Len(t, lsp.buys(), 3)
}

// A manual LSPS1 order is refused while the live check found an LSPS1 bound
// worse than announced, until the user accepts it at its live value; worse
// LSPS2 values do not hold up an LSPS1 order.
func TestManualLSPS1OrderAsksAgainForWorseBounds(t *testing.T) {
	m, ln, _ := gateManager(t, nil)
	fields := []niplsp.FieldDiff{
		{Field: "max_channel_expiry_blocks", Announced: "12960", Live: "4320", Verdict: niplsp.Worse},
		{Field: "min_funding_confirms_within_blocks", Announced: "6", Live: "6", Verdict: niplsp.Same},
		{Field: "lsps2_fee[0].min_fee", Announced: "1", Live: "2", Verdict: niplsp.Worse},
	}
	m.SetLiveChecker(func(context.Context, string) (*LiveCheck, error) {
		return &LiveCheck{Usable: true, Worse: true, Fields: fields}, nil
	})
	order := lsps1.OrderParams{LspBalanceLoki: 100_000}
	_, err := m.CreateLSPS1Order(context.TODO(), gateLSP, order, nil, nil)
	var tw *TermsWorseError
	require.ErrorAs(t, err, &tw)
	require.Len(t, tw.Terms.Fields, 2, "LSPS1 values only")
	assert.Equal(t, "max_channel_expiry_blocks", tw.Terms.Fields[0].Field)
	_, err = m.CreateLSPS1Order(context.TODO(), gateLSP, order, nil, []AcceptedTerm{{Field: "max_channel_expiry_blocks", Live: "2016"}})
	assert.ErrorIs(t, err, ErrTermsWorse)
	assert.Zero(t, sent(ln), "no order sent")

	// Accepted, or only LSPS2 worse: the order goes out.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = m.CreateLSPS1Order(ctx, gateLSP, order, nil, []AcceptedTerm{{Field: "max_channel_expiry_blocks", Live: "4320"}})
	assert.ErrorIs(t, err, context.DeadlineExceeded, "sent, no answer in this test")
	fields = fields[1:]
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	_, err = m.CreateLSPS1Order(ctx2, gateLSP, order, nil, nil)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 2, sent(ln))
}

// The JIT fee is the larger of min_fee and the proportional fee rounded up; a
// payment outside the row's size bounds, or one the fee would take whole, is
// refused.
func TestJITFeeBounds(t *testing.T) {
	p := lsps2.OpeningFeeParams{MinFeeMloki: 1000, Proportional: 10_000, MinPaymentSizeMloki: 1001, MaxPaymentSizeMloki: 1_000_000}
	for _, c := range []struct {
		amount uint64
		fee    uint64
		ok     bool
	}{
		{1000, 0, false},         // below min_payment_size
		{1001, 1000, true},       // at min_payment_size, min_fee
		{100_000, 1000, true},    // 1% is exactly min_fee
		{100_001, 1001, true},    // proportional rounded up
		{1_000_000, 10000, true}, // at max_payment_size
		{1_000_001, 0, false},    // above max_payment_size
	} {
		fee, err := jitFee(c.amount, p)
		if !c.ok {
			assert.Error(t, err, c.amount)
			continue
		}
		require.NoError(t, err, c.amount)
		assert.Equal(t, c.fee, fee, c.amount)
	}
	// A fee equal to the payment, or above it, takes it whole.
	whole := lsps2.OpeningFeeParams{MinFeeMloki: 5000, MinPaymentSizeMloki: 1}
	_, err := jitFee(5000, whole)
	assert.Error(t, err)
	fee, err := jitFee(5001, whole)
	require.NoError(t, err)
	assert.Equal(t, uint64(5000), fee)
	_, err = jitFee(1<<63, lsps2.OpeningFeeParams{Proportional: 1 << 31})
	assert.Error(t, err, "overflow")
}

// The order screens' comparison: the LSPS2 fee row offered and the LSPS1
// bounds, each against the announced values, worse only where the live value
// is worse for the buyer.
func TestOrderScreenTermsCompareWithTheAnnouncement(t *testing.T) {
	m, _, _ := gateManager(t, func(l *persist.LSP) {
		o, _ := json.Marshal([]niplsp.Option{{Name: "max_channel_expiry_blocks", Value: "12960"}, {Name: "min_channel_balance", Value: "100000"}})
		l.LSPS1 = string(o)
	})
	same, err := m.LSPS2Terms(gateLSP, liveRow())
	require.NoError(t, err)
	assert.False(t, same.Worse)
	assert.Len(t, same.Fields, 6)
	worse := liveRow()
	worse.MaxPaymentSizeMloki--
	w, err := m.LSPS2Terms(gateLSP, worse)
	require.NoError(t, err)
	assert.True(t, w.Worse)

	opts := lsps1.Options{MaxChannelExpiryBlocks: 12960, MinChannelBalanceLoki: 100000}
	l1, err := m.LSPS1Terms(gateLSP, opts)
	require.NoError(t, err)
	assert.False(t, l1.Worse)
	opts.MaxChannelExpiryBlocks = 12959
	l1, err = m.LSPS1Terms(gateLSP, opts)
	require.NoError(t, err)
	assert.True(t, l1.Worse)
	assert.Contains(t, l1.Fields, TermField{Field: "max_channel_expiry_blocks", Announced: "12960", Live: "12959", Verdict: "worse"})
	opts.MaxChannelExpiryBlocks, opts.MinChannelBalanceLoki = 12961, 99999
	l1, err = m.LSPS1Terms(gateLSP, opts)
	require.NoError(t, err)
	assert.False(t, l1.Worse, "better on both")
}
