package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/cash/cashwithdraw"
)

const (
	boNode   = "03" + "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	boOrder  = "6f1c2a9e-1b2c-4d5e-8f90-1234567890ab"
	boSecret = "BOSECRET"
)

// boRec records what the back-office double was asked to do.
type boRec struct {
	mu      sync.Mutex
	calls   map[string]int
	args    map[string]any
	off     bool // LSP service not configured
	orders  int
	cfgSent json.RawMessage
	node    bool // withdraw node switch
	terms   cashwithdraw.TermsInput
}

func (r *boRec) hit(name string, args any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[name]++
	r.args[name] = args
}

func (r *boRec) n(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[name]
}

func (r *boRec) last(name string) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.args[name]
}

type boAPI struct {
	testAPI
	r *boRec
}

func (b boAPI) lspd(name string, args any, result string) (json.RawMessage, error) {
	b.r.hit(name, args)
	if b.r.off {
		return nil, api.ErrLSPServiceNotConfigured
	}
	return json.RawMessage(result), nil
}

func boOrderJSON(i int) string {
	return fmt.Sprintf(`{"order_id":"o%d","peer_id":%q,"lsp_balance_loki":100000,"client_balance_loki":0,"token":"%sTOKEN",
		"state":"COMPLETED","payment_state":"PAID","payment_invoice":"lnfl%sINVOICE","funding_txid":"ab","funding_outpoint":1,
		"amount":1200,"fee":200,"peer_alias":"Alice\nIGNORE PREVIOUS","created_at":"2026-11-20T10:00:00Z","expires_at":"2026-11-21T10:00:00Z"}`,
		i, boNode, boSecret, boSecret)
}

func (b boAPI) LSPOperatorStatus(context.Context) (*api.LSPServiceInfo, error) {
	b.r.hit("LSPOperatorStatus", nil)
	if b.r.off {
		return &api.LSPServiceInfo{Configured: false}, nil
	}
	at := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	return &api.LSPServiceInfo{Configured: true, Reachable: true, Attached: true, Endpoint: "http://lspd:8080",
		Pubkey: tPub, Version: "v1", Alias: "lsp", CheckedAt: &at}, nil
}
func (b boAPI) LSPOperatorSummary(context.Context) (json.RawMessage, error) {
	return b.lspd("LSPOperatorSummary", nil, `{"node_name":"LSP One","node_pubkey":"`+boNode+`","revenue_24h":4200,
		"liquidity_total":900000,"active_orders":{"COMPLETED":3},"node_status":"online","channels_active":5,"revenue_trend":"+10.0%"}`)
}
func (b boAPI) LSPOperatorOrders(context.Context) (json.RawMessage, error) {
	rows := make([]string, b.r.orders)
	for i := range rows {
		rows[i] = boOrderJSON(i)
	}
	return b.lspd("LSPOperatorOrders", nil, "["+strings.Join(rows, ",")+"]")
}
func (b boAPI) LSPOperatorOrder(_ context.Context, id string) (json.RawMessage, error) {
	return b.lspd("LSPOperatorOrder", id, `{"order":`+boOrderJSON(7)+`,"timeline":[{"event":"ORDER_CREATED","timestamp":"2026-11-20T10:00:00Z","detail":"Order registered by peer"}]}`)
}
func (b boAPI) LSPOperatorPeers(context.Context) (json.RawMessage, error) {
	return b.lspd("LSPOperatorPeers", nil, `[{"pub_key":"`+boNode+`","alias":"Bob","color":"#fff","address":"1.2.3.4:9735","inbound":true,"active":true,"in_loki":5,"out_loki":6,"order_count":2}]`)
}
func (b boAPI) LSPOperatorJitChannels(context.Context) (json.RawMessage, error) {
	return b.lspd("LSPOperatorJitChannels", nil, `[{"order_id":"o1","channel_id":"812x1x0","peer_id":"`+boNode+`","capacity":100000,"fee":200,"status":"ACTIVE","created_at":"2026-11-20T10:00:00Z","txid":"ab"}]`)
}
func (b boAPI) LSPOperatorNodes(context.Context) (json.RawMessage, error) {
	return b.lspd("LSPOperatorNodes", nil, `{"nodes":[{"node_id":"`+boNode+`","name":"LSP One"}]}`)
}
func (b boAPI) LSPOperatorAnalytics(context.Context) (json.RawMessage, error) {
	return b.lspd("LSPOperatorAnalytics", nil, `{"total_revenue":9000,"success_rate":75.5,"avg_order_size":300,"active_peers":4,
		"revenue_history":[{"date":"2026-11-20","day":"Fri","revenue":100,"count":1,"volume":100}],
		"order_distribution":[{"name":"COMPLETED","value":3,"color":"#00fd87"}]}`)
}
func (b boAPI) LSPOperatorConfig(_ context.Context, node string) (json.RawMessage, error) {
	// A misbehaving lspd that leaks secrets: the tool must still not show them.
	return b.lspd("LSPOperatorConfig", node, `{"config":{"name":"LSP One","nodePubkey":"`+boNode+`","baseFeeMloki":"1000",
		"feeRate":"0.001","jitEnabled":true,"flspdPrivateKey":"`+boSecret+`KEY","tokens":["`+boSecret+`TOK"],"flnd":{"macaroon":"`+boSecret+`MAC"}},
		"announce_relays":["wss://relay.example"]}`)
}
func (b boAPI) LSPOperatorUpdateConfig(_ context.Context, node string, cfg json.RawMessage) (json.RawMessage, error) {
	b.r.mu.Lock()
	b.r.cfgSent = cfg
	b.r.mu.Unlock()
	return b.lspd("LSPOperatorUpdateConfig", node, `{"status":"updated"}`)
}
func boAnnounce(status string) string {
	return `{"node_pubkey":"` + boNode + `","enabled":true,"status":"` + status + `","updated_at":null,"relays":["wss://relay.example"],
		"publisher":{"last_event":{"id":"ev1","created_at":"x","expiration":"y","status":"listed","relays":[{"relay":"wss://relay.example","accepted":false,"error":"rate-limited"}]},
		"self_check":{"checked_at":"z","ok":true,"problems":[]},"blocked":"","last_error":""}}`
}
func (b boAPI) LSPOperatorAnnounceState(_ context.Context, node string) (json.RawMessage, error) {
	return b.lspd("LSPOperatorAnnounceState", node, boAnnounce("listed"))
}
func (b boAPI) LSPOperatorSetAnnounceState(_ context.Context, action, node string) (json.RawMessage, error) {
	return b.lspd("LSPOperatorSetAnnounceState", action+"|"+node, boAnnounce("paused"))
}
func (b boAPI) LSPOperatorRecheckAnnouncement(_ context.Context, node string) (json.RawMessage, error) {
	return b.lspd("LSPOperatorRecheckAnnouncement", node, boAnnounce("listed"))
}

func (b boAPI) GetCashHubWithdrawTerms(hub uint) (*cashwithdraw.TermsView, error) {
	b.r.hit("GetCashHubWithdrawTerms", hub)
	if hub != covCashHub {
		return nil, cashwithdraw.ErrNotCashHub
	}
	b.r.mu.Lock()
	defer b.r.mu.Unlock()
	return &cashwithdraw.TermsView{TermsInput: b.r.terms, FinalDepth: 6, Capable: true}, nil
}
func (b boAPI) SetCashHubWithdrawTerms(hub uint, in cashwithdraw.TermsInput) (*cashwithdraw.TermsView, error) {
	b.r.hit("SetCashHubWithdrawTerms", hub)
	b.r.mu.Lock()
	b.r.terms = in
	b.r.mu.Unlock()
	return &cashwithdraw.TermsView{TermsInput: in, Offered: in.Enabled}, nil
}
func (b boAPI) ListCashHubPayouts(hub uint) ([]cashwithdraw.PayoutRow, error) {
	b.r.hit("ListCashHubPayouts", hub)
	rows := make([]cashwithdraw.PayoutRow, 130)
	for i := range rows {
		rows[i] = cashwithdraw.PayoutRow{ID: uint(i + 1), State: "final", Address: "fl1qaddr", AmountMillis: 1000}
	}
	return rows, nil
}
func (b boAPI) GetCashHubPayout(hub, id uint) (*cashwithdraw.PayoutDetail, error) {
	b.r.hit("GetCashHubPayout", [2]uint{hub, id})
	return &cashwithdraw.PayoutDetail{PayoutRow: cashwithdraw.PayoutRow{ID: id, State: "final"},
		Versions: []cashwithdraw.PayoutVersion{{Seq: 1, Kind: "initial", Inputs: []string{"ab:0"}}}, Facts: []cashwithdraw.PayoutFact{}}, nil
}
func (b boAPI) CashWithdrawStatus() (*cashwithdraw.EngineStatus, error) {
	b.r.hit("CashWithdrawStatus", nil)
	return &cashwithdraw.EngineStatus{Ready: true, Payouts: map[string]int64{"final": 2}}, nil
}
func (b boAPI) CashWithdrawNode() (*api.CashWithdrawNodeState, error) {
	b.r.hit("CashWithdrawNode", nil)
	b.r.mu.Lock()
	defer b.r.mu.Unlock()
	return &api.CashWithdrawNodeState{Enabled: b.r.node, Capable: true}, nil
}
func (b boAPI) SetCashWithdrawNodeEnabled(on bool) error {
	b.r.hit("SetCashWithdrawNodeEnabled", on)
	b.r.mu.Lock()
	b.r.node = on
	b.r.mu.Unlock()
	return nil
}

func newBackoffice() (*Server, *boRec) {
	rec := &boRec{calls: map[string]int{}, args: map[string]any{}, orders: 150,
		terms: cashwithdraw.TermsInput{Enabled: false, FeeBaseMillis: 1000, FeePpm: 500}}
	return NewServer(boAPI{r: rec}, &fakeAuth{}), rec
}

// TestBackofficeReadTools: each read reaches its api method with the mapped
// argument, shows lspd data through the views (no order token, payment
// invoice, config key, macaroon or lspd endpoint), pages are clamped, and
// invalid ids never reach the core.
func TestBackofficeReadTools(t *testing.T) {
	s, rec := newBackoffice()
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()

	ok := func(name, method string, args map[string]any) string {
		t.Helper()
		before := rec.n(method)
		r := callTool(t, cs, name, args)
		if r.IsError {
			t.Fatalf("%s errored: %s", name, resultText(r))
		}
		if rec.n(method) != before+1 {
			t.Fatalf("%s did not reach %s", name, method)
		}
		out := scJSON(t, r.StructuredContent)
		for _, leak := range []string{boSecret, "lspd:8080", "payment_invoice", `"token"`} {
			if strings.Contains(out, leak) {
				t.Errorf("%s leaks %q: %s", name, leak, out)
			}
		}
		return out
	}
	bad := func(name, method string, args map[string]any) {
		t.Helper()
		before := rec.n(method)
		if r := callTool(t, cs, name, args); !r.IsError {
			t.Errorf("%s accepted invalid args %v", name, args)
		}
		if rec.n(method) != before {
			t.Errorf("%s: invalid args reached %s", name, method)
		}
	}
	has := func(name, out string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %s in %s", name, w, out)
			}
		}
	}

	has("lsp_status", ok("lsp_status", "LSPOperatorStatus", nil), `"attached":true`, `"admin_pubkey":"`+tPub+`"`, `"checked_at":"2026-11-20T00:00:00Z"`)
	has("lsp_summary", ok("lsp_summary", "LSPOperatorSummary", nil), `"revenue_24h_loki":4200`, `"orders_by_state":{"COMPLETED":3}`, `"node_name_untrusted":"LSP One"`)

	out := ok("lsp_orders", "LSPOperatorOrders", map[string]any{"limit": 500})
	var orders lspOrdersPage
	_ = json.Unmarshal([]byte(out), &orders)
	if orders.Total != 150 || len(orders.Orders) != maxPageLimit || orders.Limit != maxPageLimit {
		t.Errorf("lsp_orders not clamped: total=%d rows=%d limit=%d", orders.Total, len(orders.Orders), orders.Limit)
	}
	if o := orders.Orders[0]; o.OrderID != "o0" || o.AmountLoki != 1200 || o.FeeLoki != 200 || o.State != "COMPLETED" || strings.Contains(o.PeerAlias, "\n") {
		t.Errorf("lsp_orders mapped wrong: %+v", o)
	}
	out = ok("lsp_orders", "LSPOperatorOrders", map[string]any{"limit": 10, "offset": 145})
	_ = json.Unmarshal([]byte(out), &orders)
	if len(orders.Orders) != 5 || orders.Orders[0].OrderID != "o145" {
		t.Errorf("lsp_orders offset: %+v", orders.Orders)
	}

	has("lsp_order", ok("lsp_order", "LSPOperatorOrder", map[string]any{"order_id": boOrder}), `"order_id":"o7"`, "ORDER_CREATED")
	if rec.last("LSPOperatorOrder") != boOrder {
		t.Errorf("lsp_order id = %v", rec.last("LSPOperatorOrder"))
	}
	bad("lsp_order", "LSPOperatorOrder", map[string]any{"order_id": "../admin"})
	bad("lsp_order", "LSPOperatorOrder", map[string]any{"order_id": ""})

	has("lsp_peers", ok("lsp_peers", "LSPOperatorPeers", nil), `"pub_key":"`+boNode+`"`, `"alias_untrusted":"Bob"`, `"order_count":2`)
	has("lsp_jit_channels", ok("lsp_jit_channels", "LSPOperatorJitChannels", nil), `"channel_id":"812x1x0"`, `"capacity_loki":100000`)
	has("lsp_nodes", ok("lsp_nodes", "LSPOperatorNodes", nil), `"node_id":"`+boNode+`"`)
	has("lsp_analytics", ok("lsp_analytics", "LSPOperatorAnalytics", nil), `"total_revenue_loki":9000`, `{"count":3,"state":"COMPLETED"}`, `"revenue_loki":100`)

	out = ok("lsp_config", "LSPOperatorConfig", map[string]any{"node": boNode})
	has("lsp_config", out, `"baseFeeMloki":"1000"`, `"jitEnabled":true`, "wss://relay.example")
	if strings.Contains(out, "flspdPrivateKey") || strings.Contains(out, "tokens") || strings.Contains(out, "flnd") {
		t.Errorf("lsp_config shows a non-allow-listed field: %s", out)
	}
	if rec.last("LSPOperatorConfig") != boNode {
		t.Errorf("lsp_config node = %v", rec.last("LSPOperatorConfig"))
	}
	bad("lsp_config", "LSPOperatorConfig", map[string]any{"node": "zz"})

	has("lsp_announce_state", ok("lsp_announce_state", "LSPOperatorAnnounceState", nil), `"status":"listed"`, `"error":"rate-limited"`)
	bad("lsp_announce_state", "LSPOperatorAnnounceState", map[string]any{"node": boNode + "00"})

	// Withdrawals.
	has("withdraw_terms", ok("withdraw_terms", "GetCashHubWithdrawTerms", map[string]any{"hub_app_id": covCashHub}), `"fee_ppm":500`, `"final_depth":6`)
	if rec.last("GetCashHubWithdrawTerms") != covCashHub {
		t.Errorf("withdraw_terms hub = %v", rec.last("GetCashHubWithdrawTerms"))
	}
	bad("withdraw_terms", "GetCashHubWithdrawTerms", map[string]any{"hub_app_id": 0})

	out = ok("list_payouts", "ListCashHubPayouts", map[string]any{"hub_app_id": covCashHub, "limit": 1000})
	var p payoutsPage
	_ = json.Unmarshal([]byte(out), &p)
	if p.Total != 130 || len(p.Payouts) != maxPageLimit {
		t.Errorf("list_payouts not clamped: total=%d rows=%d", p.Total, len(p.Payouts))
	}
	bad("list_payouts", "ListCashHubPayouts", map[string]any{"hub_app_id": 0})

	has("get_payout", ok("get_payout", "GetCashHubPayout", map[string]any{"hub_app_id": covCashHub, "payout_id": 4}), `"id":4`, `"inputs":["ab:0"]`)
	if got := rec.last("GetCashHubPayout"); got != [2]uint{covCashHub, 4} {
		t.Errorf("get_payout args = %v", got)
	}
	bad("get_payout", "GetCashHubPayout", map[string]any{"hub_app_id": covCashHub, "payout_id": 0})

	has("cash_withdraw_status", ok("cash_withdraw_status", "CashWithdrawStatus", nil), `"ready":true`, `"final":2`)
	has("get_withdraw_node", ok("get_withdraw_node", "CashWithdrawNode", nil), `"capable":true`)
}

// TestBackofficeFeatureOff: with no lspd attached the tools stay listed and
// answer plainly; lsp_status reports configured=false.
func TestBackofficeFeatureOff(t *testing.T) {
	s, rec := newBackoffice()
	rec.off = true
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()
	r := callTool(t, cs, "lsp_status", nil)
	if r.IsError || !strings.Contains(scJSON(t, r.StructuredContent), `"configured":false`) {
		t.Fatalf("lsp_status off: %s", resultText(r))
	}
	for _, name := range []string{"lsp_summary", "lsp_orders", "lsp_peers", "lsp_jit_channels", "lsp_nodes", "lsp_analytics", "lsp_config", "lsp_announce_state"} {
		r := callTool(t, cs, name, nil)
		if !r.IsError || !strings.Contains(resultText(r), "not configured") {
			t.Errorf("%s with lspd off: %s", name, resultText(r))
		}
	}
	if r := callTool(t, cs, "put_lsp_config", map[string]any{"idempotency_key": "off", "changes": map[string]any{"baseFeeMloki": 1}}); !r.IsError {
		t.Error("put_lsp_config previewed without an LSP service")
	}
	if rec.n("LSPOperatorUpdateConfig") != 0 {
		t.Error("config update attempted with lspd off")
	}
}

// TestBackofficeRecheck: the plain write reaches the core once per key.
func TestBackofficeRecheck(t *testing.T) {
	s, rec := newBackoffice()
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()
	for i := 0; i < 2; i++ {
		r := callTool(t, cs, "lsp_recheck_announcement", map[string]any{"idempotency_key": "rc1", "node": boNode})
		if r.IsError || !strings.Contains(scJSON(t, r.StructuredContent), `"status":"listed"`) {
			t.Fatalf("recheck: %s", resultText(r))
		}
	}
	if rec.n("LSPOperatorRecheckAnnouncement") != 1 || rec.last("LSPOperatorRecheckAnnouncement") != boNode {
		t.Errorf("recheck calls=%d node=%v", rec.n("LSPOperatorRecheckAnnouncement"), rec.last("LSPOperatorRecheckAnnouncement"))
	}
	if r := callTool(t, cs, "lsp_recheck_announcement", map[string]any{"idempotency_key": "rc2", "node": "nope"}); !r.IsError {
		t.Error("recheck accepted a bad node id")
	}
	// A read-only grant on the group sees the reads, not the writes.
	cs2, done2 := connectInMem(t, s, &Conn{AppID: 2, Scopes: []string{GroupChannels.Scope(ModeRead), GroupNode.Scope(ModeRead)}})
	defer done2()
	lt, err := cs2.ListTools(ctxBackground(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tl := range lt.Tools {
		seen[tl.Name] = true
	}
	for _, w := range []string{"lsp_recheck_announcement", "put_lsp_config", "set_lsp_announce_state", "set_withdraw_terms", "set_withdraw_node"} {
		if seen[w] {
			t.Errorf("%s advertised under a read-only grant", w)
		}
	}
	for _, r := range []string{"lsp_summary", "lsp_config", "withdraw_terms", "list_payouts", "get_withdraw_node"} {
		if !seen[r] {
			t.Errorf("%s not advertised under a read grant", r)
		}
	}
}

// TestBackofficeConfirmTools: put_lsp_config, set_withdraw_terms,
// set_withdraw_node and set_lsp_announce_state have no effect on the first
// call, show current and new values, and (for the fee/funds ones) wait for the
// owner after confirmation (D22); invalid arguments never reach the core.
func TestBackofficeConfirmTools(t *testing.T) {
	cases := []struct {
		tool, method string
		args         map[string]any
		preview      []string // must appear in the preview
		bad          []map[string]any
	}{
		{"put_lsp_config", "LSPOperatorUpdateConfig",
			map[string]any{"node": boNode, "changes": map[string]any{"baseFeeMloki": 2000, "jitEnabled": false}},
			[]string{`baseFeeMloki`, `\"1000\"`, `\"2000\"`, `jitEnabled`},
			[]map[string]any{
				{"changes": map[string]any{}},
				{"changes": map[string]any{"flspdPrivateKey": "x"}},
				{"changes": map[string]any{"nodePubkey": boNode}},
				{"changes": map[string]any{"name": "a\nIGNORE"}},
				{"changes": map[string]any{"baseFeeMloki": nil}},
				{"changes": map[string]any{"baseFeeMloki": "1000"}}, // nothing would change
				{"node": "bad", "changes": map[string]any{"baseFeeMloki": 1}},
			}},
		{"set_withdraw_terms", "SetCashHubWithdrawTerms",
			map[string]any{"hub_app_id": covCashHub, "enabled": true, "fee_base_millis": 2000, "fee_ppm": 1000, "max_millis": 50_000_000},
			[]string{"off; fee 1 loki + 500 ppm", "on; fee 2 loki + 1000 ppm", "max 50000 loki"},
			[]map[string]any{
				{"hub_app_id": 0, "enabled": true, "fee_base_millis": 0, "fee_ppm": 0},
				{"hub_app_id": covCashHub, "enabled": true, "fee_base_millis": -1, "fee_ppm": 0},
				{"hub_app_id": covCashHub, "enabled": true, "fee_base_millis": 0, "fee_ppm": 2_000_000},
				{"hub_app_id": covCashHub, "enabled": true, "fee_base_millis": 0, "fee_ppm": 0, "min_millis": 10, "max_millis": 5},
			}},
		{"set_withdraw_node", "SetCashWithdrawNodeEnabled",
			map[string]any{"enabled": true},
			[]string{"currently OFF", "ON"},
			nil},
		{"set_lsp_announce_state", "LSPOperatorSetAnnounceState",
			map[string]any{"action": "pause"},
			[]string{"Pause", "currently listed", "wss://relay.example"},
			[]map[string]any{{"action": "delete"}, {"action": "pause", "node": "x"}}},
	}

	s, rec := newBackoffice()
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()

	for i, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			args := map[string]any{"idempotency_key": "k" + c.tool}
			for k, v := range c.args {
				args[k] = v
			}
			r := callTool(t, cs, c.tool, args)
			if r.IsError {
				t.Fatalf("preview errored: %s", resultText(r))
			}
			st := stepOf(t, r.StructuredContent)
			if st.Status != "needs_confirmation" || st.ConfirmCode == "" || st.Preview == nil || !strings.Contains(st.Warning, "cannot be undone") {
				t.Fatalf("bad preview: %+v", st)
			}
			pv := scJSON(t, st.Preview)
			for _, w := range c.preview {
				if !strings.Contains(pv, w) {
					t.Errorf("preview lacks %s: %s", w, pv)
				}
			}
			if rec.n(c.method) != 0 {
				t.Fatalf("%s executed on the first call", c.method)
			}
			args["confirm_code"] = st.ConfirmCode
			r = callTool(t, cs, c.tool, args)
			if r.IsError {
				t.Fatalf("confirm failed: %s", resultText(r))
			}
			if cst := stepOf(t, r.StructuredContent); cst.Status == "pending_approval" {
				if !alwaysOwner[c.tool] {
					t.Fatalf("%s queued for the owner but is not owner-gated", c.tool)
				}
				if rec.n(c.method) != 0 {
					t.Fatalf("%s executed before owner approval", c.method)
				}
				if _, err := s.Pending().Approve(cst.ActionID); err != nil {
					t.Fatalf("owner approve: %v", err)
				}
				r = callTool(t, cs, c.tool, args) // retry reports the outcome
				if got := stepOf(t, r.StructuredContent).Status; got != "executed" {
					t.Fatalf("after approval status = %s", got)
				}
			} else if cst.Status != "executed" {
				t.Fatalf("confirm status = %s", cst.Status)
			} else if alwaysOwner[c.tool] {
				t.Fatalf("%s executed without the owner", c.tool)
			}
			if rec.n(c.method) != 1 {
				t.Fatalf("%s calls = %d, want 1", c.method, rec.n(c.method))
			}
			for j, b := range c.bad {
				ba := map[string]any{"idempotency_key": fmt.Sprintf("bad%d-%d", i, j)}
				for k, v := range b {
					ba[k] = v
				}
				if r := callTool(t, cs, c.tool, ba); !r.IsError {
					t.Errorf("accepted invalid args %v", b)
				}
			}
			if rec.n(c.method) != 1 {
				t.Errorf("invalid args reached the core (%d calls)", rec.n(c.method))
			}
		})
	}

	for _, tool := range []string{"put_lsp_config", "set_withdraw_terms", "set_withdraw_node"} {
		if !alwaysOwner[tool] {
			t.Errorf("%s is not owner-approved (D22)", tool)
		}
	}

	// put_lsp_config sent the CURRENT allow-listed config with only the named
	// fields changed (in lspd's string form), and nothing secret.
	var sent map[string]any
	if err := json.Unmarshal(rec.cfgSent, &sent); err != nil {
		t.Fatalf("config sent: %v (%s)", err, rec.cfgSent)
	}
	if sent["baseFeeMloki"] != "2000" || sent["jitEnabled"] != false || sent["feeRate"] != "0.001" || sent["name"] != "LSP One" {
		t.Errorf("merged config wrong: %s", rec.cfgSent)
	}
	if strings.Contains(string(rec.cfgSent), boSecret) || strings.Contains(string(rec.cfgSent), "flspdPrivateKey") {
		t.Errorf("config sent carries lspd secrets: %s", rec.cfgSent)
	}
	if rec.last("LSPOperatorUpdateConfig") != boNode {
		t.Errorf("config node = %v", rec.last("LSPOperatorUpdateConfig"))
	}
	if rec.terms.FeeBaseMillis != 2000 || rec.terms.FeePpm != 1000 || !rec.terms.Enabled || rec.terms.MaxMillis == nil || *rec.terms.MaxMillis != 50_000_000 || rec.terms.MinMillis != nil {
		t.Errorf("withdraw terms mapped wrong: %+v", rec.terms)
	}
	if rec.last("SetCashHubWithdrawTerms") != covCashHub || rec.last("SetCashWithdrawNodeEnabled") != true {
		t.Errorf("withdraw args: hub=%v node=%v", rec.last("SetCashHubWithdrawTerms"), rec.last("SetCashWithdrawNodeEnabled"))
	}
	if rec.last("LSPOperatorSetAnnounceState") != "pause|" {
		t.Errorf("announce args = %v", rec.last("LSPOperatorSetAnnounceState"))
	}

	// A preview whose current values changed before confirmation is refused.
	args := map[string]any{"idempotency_key": "stale", "hub_app_id": covCashHub, "enabled": false, "fee_base_millis": 0, "fee_ppm": 0}
	st := stepOf(t, callTool(t, cs, "set_withdraw_terms", args).StructuredContent)
	rec.mu.Lock()
	rec.terms.FeePpm = 777
	rec.mu.Unlock()
	args["confirm_code"] = st.ConfirmCode
	if r := callTool(t, cs, "set_withdraw_terms", args); !r.IsError {
		t.Error("confirmation accepted after the current terms changed")
	}
}
