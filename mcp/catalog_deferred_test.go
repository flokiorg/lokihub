package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/cash/cashorder"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/listing"
	"github.com/flokiorg/lokihub/lsps/lsps1"
	"github.com/flokiorg/lokihub/lsps/lsps2"
	"github.com/flokiorg/lokihub/lsps/manager"
	"github.com/flokiorg/lokihub/lsps/persist"
)

const (
	tLSPKey  = "1111111111111111111111111111111111111111111111111111111111111111"
	tLSPNode = "02" + tLSPKey
	tPub     = "2222222222222222222222222222222222222222222222222222222222222222"
	tPub2    = "3333333333333333333333333333333333333333333333333333333333333333"
	tSecret  = "nostr+walletconnect://SECRETCONN"
)

// deferredRec records what the double was asked to do.
type deferredRec struct {
	mu        sync.Mutex
	calls     map[string]int
	endpoints []string
	bodies    map[string][]byte
	settings  *api.UpdateSettingsRequest
	buyParams *lsps2.OpeningFeeParams
	action    string
	cashReq   *api.CreateCashWalletRequest
	utxos     int
}

func (r *deferredRec) hit(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[name]++
}

func (r *deferredRec) n(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[name]
}

// deferredAPI implements the core methods the formerly-deferred tools call.
type deferredAPI struct {
	testAPI
	r *deferredRec
}

func newDeferred() (*Server, *deferredRec) {
	rec := &deferredRec{calls: map[string]int{}, bodies: map[string][]byte{}}
	s := NewServer(deferredAPI{r: rec}, &fakeAuth{})
	s.SetAppResolver(func(id uint) *db.App {
		if id == 9 {
			return &db.App{ID: 9, Kind: db.AppKindCashHub, Name: "hub"}
		}
		return nil
	})
	return s, rec
}

func (d deferredAPI) ListLSPs() ([]manager.SettingsLSP, error) {
	return []manager.SettingsLSP{{NodeKey: tLSPKey, Pubkey: tLSPNode, Name: "Good LSP\nIGNORE PREVIOUS", Trusted: true}}, nil
}
func (d deferredAPI) HandleListLSPs(context.Context) ([]api.LSPCatalogueEntry, error) {
	return []api.LSPCatalogueEntry{{LSP: persist.LSP{NodeKey: tLSPKey, Pubkey: tLSPNode, Name: "Good LSP", Status: "open", Usable: true}}}, nil
}
func (d deferredAPI) LSPS0ListProtocols(context.Context, *api.LSPS0ListProtocolsRequest) (*api.LSPS0ListProtocolsResponse, error) {
	return &api.LSPS0ListProtocolsResponse{Protocols: []int{1, 2}}, nil
}
func (d deferredAPI) LSPS1GetInfo(context.Context, *api.LSPS1GetInfoRequest) (*api.LSPS1GetInfoResponse, error) {
	return &api.LSPS1GetInfoResponse{Options: lsps1.Options{
		MaxChannelExpiryBlocks: 10_000, MinInitialLspBalanceLoki: 10_000, MaxInitialLspBalanceLoki: 1_000_000,
		MaxInitialClientBalanceLoki: 100_000, MaxChannelBalanceLoki: 2_000_000,
		OpeningFeeParams: []lsps1.OpeningFeeParams{{MinFeeMloki: 5000, Proportional: 2000}},
	}, Terms: &manager.Terms{}}, nil
}
func (d deferredAPI) LSPS1GetOrder(context.Context, *api.LSPS1GetOrderRequest) (interface{}, error) {
	return map[string]interface{}{"order_id": "ord1", "state": "CREATED", "payment_invoice": "lnfl1x", "fee_total_loki": uint64(12)}, nil
}
func (d deferredAPI) LSPS1CreateOrder(context.Context, *api.LSPS1CreateOrderRequest) (interface{}, error) {
	d.r.hit("LSPS1CreateOrder")
	return map[string]interface{}{"order_id": "ord2", "payment_invoice": "lnfl1y", "fee_total_loki": uint64(7)}, nil
}
func liveFee() *api.LSPS2GetInfoResponse {
	return &api.LSPS2GetInfoResponse{OpeningFeeParams: lsps2.OpeningFeeParams{
		MinFeeMloki: 10_000, Proportional: 1000, ValidUntil: time.Now().Add(time.Hour),
		MinPaymentSizeMloki: 1000, MaxPaymentSizeMloki: 100_000_000, Promise: "PROMISE-OPAQUE",
	}, Terms: &manager.Terms{}}
}
func (d deferredAPI) LSPS2GetInfo(context.Context, *api.LSPS2GetInfoRequest) (interface{}, error) {
	return liveFee(), nil
}
func (d deferredAPI) LSPS2Buy(_ context.Context, req *api.LSPS2BuyRequest) (*api.LSPS2BuyResponse, error) {
	d.r.hit("LSPS2Buy")
	d.r.mu.Lock()
	p := req.OpeningFeeParams
	d.r.buyParams = &p
	d.r.mu.Unlock()
	return &api.LSPS2BuyResponse{InterceptSCID: 42, CLTVExpiryDelta: 144, LSPNodeID: tLSPNode}, nil
}
func (d deferredAPI) LSPS5ListWebhooks(context.Context, *api.LSPS5ListWebhooksRequest) (interface{}, error) {
	return map[string]string{"requestId": "rq1"}, nil
}
func (d deferredAPI) HandleAddLSP(context.Context, *api.AddLSPRequest) (*api.LSPCatalogueEntry, error) {
	d.r.hit("HandleAddLSP")
	return &api.LSPCatalogueEntry{LSP: persist.LSP{NodeKey: tLSPKey}}, nil
}
func (d deferredAPI) HandleLSPLiveCheck(context.Context, string) (*api.LSPCatalogueEntry, error) {
	d.r.hit("HandleLSPLiveCheck")
	return &api.LSPCatalogueEntry{LSP: persist.LSP{NodeKey: tLSPKey}}, nil
}
func (d deferredAPI) HandleTrustLSP(context.Context, string, bool) (*api.LSPCatalogueEntry, error) {
	d.r.hit("HandleTrustLSP")
	return &api.LSPCatalogueEntry{LSP: persist.LSP{NodeKey: tLSPKey, Trusted: true}}, nil
}

func tListing() listing.View {
	return listing.View{ID: 7, Type: "circle_wallet", Title: "Old", Content: "kept content", Phase: "listed", Status: "open",
		Connection: tSecret, OperatorCommand: "cash discover operator add x", Languages: []string{"en"}}
}
func (d deferredAPI) ListListings(context.Context) ([]listing.View, error) {
	return []listing.View{tListing()}, nil
}
func (d deferredAPI) PreviewListing(context.Context, uint) (*nip01.Event, error) {
	return &nip01.Event{Kind: 31190, PubKey: tPub, Content: "c", Sig: "SIGSIG",
		Tags: [][]string{{"d", "x"}, {"connection", "opaque"}, {"r", tSecret}, {"title", "Hi"}}}, nil
}
func (d deferredAPI) CreateListing(_ context.Context, body []byte) (*listing.View, error) {
	d.r.hit("CreateListing")
	d.r.mu.Lock()
	d.r.bodies["create"] = body
	d.r.mu.Unlock()
	v := tListing()
	return &v, nil
}
func (d deferredAPI) UpdateListing(_ context.Context, _ uint, body []byte) (*listing.View, error) {
	d.r.hit("UpdateListing")
	d.r.mu.Lock()
	d.r.bodies["update"] = body
	d.r.mu.Unlock()
	v := tListing()
	return &v, nil
}
func (d deferredAPI) ListingAction(_ context.Context, _ uint, action string) (*listing.View, error) {
	d.r.hit("ListingAction")
	d.r.mu.Lock()
	d.r.action = action
	d.r.mu.Unlock()
	v := tListing()
	return &v, nil
}
func (d deferredAPI) PublishNodeProfile(context.Context, api.PublishNodeProfileRequest) error {
	d.r.hit("PublishNodeProfile")
	return nil
}
func (d deferredAPI) PublishNodeNote(context.Context, api.PublishNodeNoteRequest) (*api.PublishNodeNoteResponse, error) {
	d.r.hit("PublishNodeNote")
	return &api.PublishNodeNoteResponse{ID: "ev1"}, nil
}
func (d deferredAPI) GetCircleIdentityFollowSet(context.Context, uint) (*api.FollowSetResponse, error) {
	return &api.FollowSetResponse{Managed: true, Resolved: true, PublicMembers: []string{tPub2}}, nil
}
func (d deferredAPI) PublishCircleIdentityFollowSet(context.Context, uint, *api.FollowSetRequest) (*api.FollowSetResponse, error) {
	d.r.hit("PublishCircleIdentityFollowSet")
	return &api.FollowSetResponse{Managed: true}, nil
}
func (d deferredAPI) UpdateSettings(req *api.UpdateSettingsRequest) error {
	d.r.hit("UpdateSettings")
	d.r.mu.Lock()
	d.r.settings = req
	d.r.mu.Unlock()
	return nil
}
func (d deferredAPI) RequestMempoolApi(_ context.Context, endpoint string) (interface{}, error) {
	d.r.mu.Lock()
	d.r.endpoints = append(d.r.endpoints, endpoint)
	n := d.r.utxos
	d.r.mu.Unlock()
	switch {
	case endpoint == "/v1/fees/recommended":
		return map[string]any{"fastestFee": 5.0, "hourFee": 1.0}, nil
	case endpoint == "/blocks/tip/height":
		return 812.0, nil
	case strings.HasSuffix(endpoint, "/status"):
		return map[string]any{"confirmed": true, "block_height": 800.0}, nil
	default:
		u := make([]any, n)
		for i := range u {
			u[i] = map[string]any{"txid": "ab", "vout": 0.0, "value": 1000.0}
		}
		return u, nil
	}
}
func (d deferredAPI) GetApp(_ context.Context, app *db.App) *api.App {
	return &api.App{ID: app.ID, Name: app.Name, Kind: app.Kind, Balance: 5_000_000}
}
func (d deferredAPI) CreateCashWallet(_ uint, req *api.CreateCashWalletRequest) (*api.CreateCashWalletResponse, error) {
	d.r.hit("CreateCashWallet")
	d.r.mu.Lock()
	d.r.cashReq = req
	d.r.mu.Unlock()
	return &api.CreateCashWalletResponse{AppID: 11, CashToken: "cashSECRETTOKEN",
		Recipients: []api.CashWalletRecipient{{IdentityType: "pubkey", IdentityValue: tPub, AmountMloki: 1000, CashSecret: "SECRETCASH"}}}, nil
}
func (d deferredAPI) GetOrderTerms(*db.App) (*cashorder.Settings, error) { return nil, nil }
func (d deferredAPI) PutOrderTerms(*db.App, cashorder.Settings) error {
	d.r.hit("PutOrderTerms")
	return nil
}

func scJSON(t *testing.T, sc any) string {
	t.Helper()
	b, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDeferredReadTools(t *testing.T) {
	s, rec := newDeferred()
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()

	ok := func(name string, args map[string]any) string {
		t.Helper()
		r := callTool(t, cs, name, args)
		if r.IsError {
			t.Fatalf("%s errored: %s", name, resultText(r))
		}
		return scJSON(t, r.StructuredContent)
	}
	bad := func(name string, args map[string]any) {
		t.Helper()
		if r := callTool(t, cs, name, args); !r.IsError {
			t.Errorf("%s accepted invalid args %v", name, args)
		}
	}

	if out := ok("list_trusted_lsps", nil); !strings.Contains(out, tLSPKey) {
		t.Errorf("list_trusted_lsps: %s", out)
	}
	if out := ok("lsp_protocols", map[string]any{"lsp_pubkey": tLSPNode}); !strings.Contains(out, "[1,2]") {
		t.Errorf("lsp_protocols: %s", out)
	}
	bad("lsp_protocols", map[string]any{"lsp_pubkey": "zz"})
	if out := ok("lsp_channel_order_info", map[string]any{"lsp_pubkey": tLSPNode}); !strings.Contains(out, `"min_fee_mloki":5000`) {
		t.Errorf("lsp_channel_order_info: %s", out)
	}
	if out := ok("lsp_channel_order_status", map[string]any{"lsp_pubkey": tLSPNode, "order_id": "ord1"}); !strings.Contains(out, `"state":"CREATED"`) {
		t.Errorf("lsp_channel_order_status: %s", out)
	}
	bad("lsp_channel_order_status", map[string]any{"lsp_pubkey": tLSPNode, "order_id": "../x"})
	out := ok("lsp_jit_fee_info", map[string]any{"lsp_pubkey": tLSPNode})
	if !strings.Contains(out, `"min_fee_mloki":10000`) || strings.Contains(out, "PROMISE") {
		t.Errorf("lsp_jit_fee_info must show fees and never the promise: %s", out)
	}
	if out := ok("lsp_list_webhooks", map[string]any{"lsp_pubkey": tLSPNode}); !strings.Contains(out, "rq1") {
		t.Errorf("lsp_list_webhooks: %s", out)
	}

	out = ok("list_listings", nil)
	if !strings.Contains(out, `"title":"Old"`) || strings.Contains(out, "SECRETCONN") {
		t.Errorf("list_listings must not carry the connection string: %s", out)
	}
	out = ok("preview_listing", map[string]any{"listing_id": 7})
	if strings.Contains(out, "SECRETCONN") || strings.Contains(out, "opaque") || strings.Contains(out, "SIGSIG") || !strings.Contains(out, "redacted") {
		t.Errorf("preview_listing not redacted: %s", out)
	}
	bad("preview_listing", map[string]any{"listing_id": 0})

	// Explorer: only fixed endpoint shapes reach the core.
	ok("explorer_query", map[string]any{"query": "recommended_fees"})
	if out := ok("explorer_query", map[string]any{"query": "tip_height"}); !strings.Contains(out, `"tip_height":812`) {
		t.Errorf("tip_height: %s", out)
	}
	txid := strings.Repeat("AB", 32)
	ok("explorer_query", map[string]any{"query": "tx_status", "txid": txid})
	rec.mu.Lock()
	rec.utxos = maxUTXOs + 5
	rec.mu.Unlock()
	if out := ok("explorer_query", map[string]any{"query": "address_utxos", "address": "fl1qxyzxyzxyzxyzxyz"}); !strings.Contains(out, `"utxos_truncated":true`) {
		t.Errorf("utxos not capped: %s", out)
	}
	before := len(rec.endpoints)
	bad("explorer_query", map[string]any{"query": "/v1/anything"})
	bad("explorer_query", map[string]any{"query": "tx_status", "txid": "../../admin"})
	bad("explorer_query", map[string]any{"query": "tx_status", "txid": txid + "/x"})
	bad("explorer_query", map[string]any{"query": "address_utxos", "address": "fl1q/../../x?y=1"})
	if len(rec.endpoints) != before {
		t.Errorf("rejected explorer queries reached the core: %v", rec.endpoints[before:])
	}
	want := []string{"/v1/fees/recommended", "/blocks/tip/height", "/tx/" + strings.ToLower(txid) + "/status", "/address/fl1qxyzxyzxyzxyzxyz/utxo"}
	if strings.Join(rec.endpoints, " ") != strings.Join(want, " ") {
		t.Errorf("explorer endpoints = %v, want %v", rec.endpoints, want)
	}
}

func TestDeferredWriteTools(t *testing.T) {
	s, rec := newDeferred()
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()

	if r := callTool(t, cs, "update_display_settings", map[string]any{"idempotency_key": "s1", "currency": "USD"}); r.IsError {
		t.Fatalf("update_display_settings: %s", resultText(r))
	}
	if rec.settings == nil || rec.settings.Currency != "USD" || rec.settings.MempoolApi != "" || rec.settings.Relay != "" ||
		rec.settings.LokihubStoreURL != nil || rec.settings.PrivateEnvelope != nil || rec.settings.TrustedNwcRelay != nil {
		t.Errorf("settings request carries more than the allow-listed fields: %+v", rec.settings)
	}
	for _, args := range []map[string]any{
		{"idempotency_key": "s2"},
		{"idempotency_key": "s3", "currency": "US D"},
		{"idempotency_key": "s4", "mempoolApi": "http://169.254.169.254"},
	} {
		if r := callTool(t, cs, "update_display_settings", args); !r.IsError {
			t.Errorf("update_display_settings accepted %v", args)
		}
	}
	if rec.n("UpdateSettings") != 1 {
		t.Errorf("UpdateSettings calls = %d, want 1", rec.n("UpdateSettings"))
	}

	if r := callTool(t, cs, "add_lsp", map[string]any{"idempotency_key": "a1", "key": tLSPKey}); r.IsError {
		t.Fatalf("add_lsp: %s", resultText(r))
	}
	if r := callTool(t, cs, "add_lsp", map[string]any{"idempotency_key": "a2", "key": tLSPNode + "@evil.example:9735"}); !r.IsError {
		t.Error("add_lsp accepted a pubkey@host")
	}
	if r := callTool(t, cs, "lsp_live_check", map[string]any{"idempotency_key": "c1", "key": tLSPNode}); r.IsError {
		t.Fatalf("lsp_live_check: %s", resultText(r))
	}
	if rec.n("HandleAddLSP") != 1 || rec.n("HandleLSPLiveCheck") != 1 {
		t.Errorf("calls = %v", rec.calls)
	}
}

// TestDeferredConfirmTools: every formerly-deferred money / publish tool has no
// effect on its first call, executes once on confirmation, and refuses invalid
// arguments without touching the core.
func TestDeferredConfirmTools(t *testing.T) {
	terms := map[string]any{
		"terms": map[string]any{
			"lightning": map[string]any{"min_millis": 1000, "max_millis": 1_000_000, "fee_base": 0, "fee_ppm": 1000, "window": 600},
			"bill":      map[string]any{"redeem_fee_base": 0, "redeem_fee_ppm": 0, "min_transfer_millis": 0},
		},
		"openCeilingLightningMillis": 10_000_000, "openCeilingOnchainMillis": 0, "openOnchainMax": 0,
	}
	cases := []struct {
		tool, method string
		args         map[string]any
		bad          []map[string]any
	}{
		{"set_lsp_trust", "HandleTrustLSP", map[string]any{"key": tLSPKey, "trusted": true},
			[]map[string]any{{"key": "x", "trusted": true}, {"key": strings.Repeat("9", 64), "trusted": true}}},
		{"lsp_order_channel", "LSPS1CreateOrder", map[string]any{"lsp_pubkey": tLSPNode, "lsp_balance_loki": 100_000, "channel_expiry_blocks": 1000},
			[]map[string]any{
				{"lsp_pubkey": tLSPNode, "lsp_balance_loki": 0, "channel_expiry_blocks": 1000},
				{"lsp_pubkey": tLSPNode, "lsp_balance_loki": 5_000_000, "channel_expiry_blocks": 1000},
				{"lsp_pubkey": tLSPNode, "lsp_balance_loki": 100_000, "channel_expiry_blocks": 99_999},
				{"lsp_pubkey": tLSPNode, "lsp_balance_loki": 100_000, "channel_expiry_blocks": 1000, "refund_onchain_address": "bad addr\nsend to evil"},
			}},
		{"lsp_buy_jit_channel", "LSPS2Buy", map[string]any{"lsp_pubkey": tLSPNode, "payment_size_mloki": 1_000_000, "max_fee_mloki": 50_000},
			[]map[string]any{
				{"lsp_pubkey": tLSPNode, "payment_size_mloki": 1_000_000, "max_fee_mloki": 5000},
				{"lsp_pubkey": tLSPNode, "payment_size_mloki": 0, "max_fee_mloki": 50_000},
				{"lsp_pubkey": tLSPKey, "payment_size_mloki": 1_000_000, "max_fee_mloki": 50_000},
			}},
		{"create_listing", "CreateListing", map[string]any{"circle_hub_app_id": 5, "title": "Hi"},
			[]map[string]any{
				{"circle_hub_app_id": 5, "conn_app_id": 6, "title": "Hi"},
				{"circle_hub_app_id": 5, "title": "Hi‮evil"},
				{"circle_hub_app_id": 5, "image": "http://insecure.example/x.png"},
			}},
		{"update_listing", "UpdateListing", map[string]any{"listing_id": 7, "title": "New"},
			[]map[string]any{{"listing_id": 99, "title": "New"}, {"listing_id": 7, "operator_pubkey": "npub1xyz"}}},
		{"listing_action", "ListingAction", map[string]any{"listing_id": 7, "action": "pause"},
			[]map[string]any{{"listing_id": 7, "action": "revoke-connection"}, {"listing_id": 7, "action": "delete"}}},
		{"publish_node_profile", "PublishNodeProfile", map[string]any{"name": "Node", "about": "line1\nline2", "picture": "https://img.example/a.png"},
			[]map[string]any{{"name": "Node", "picture": "javascript:alert(1)"}, {"name": "a\nb"}, {}, {"name": "x", "nip05": "not an address"}}},
		{"publish_node_note", "PublishNodeNote", map[string]any{"content": "hello\nworld"},
			[]map[string]any{{"content": ""}, {"content": "\x1b[31mred"}, {"content": strings.Repeat("a", 2001)}}},
		{"publish_circle_follow_set", "PublishCircleIdentityFollowSet", map[string]any{"identity_id": 3, "title": "Members", "public_members": []any{tPub}},
			[]map[string]any{
				{"identity_id": 3, "title": "Members", "public_members": []any{"npub1abc"}},
				{"identity_id": 3, "title": "Members", "public_members": []any{tPub}, "hidden_members": []any{tPub}},
				{"identity_id": 0, "title": "Members", "public_members": []any{tPub}},
			}},
		{"create_cash_wallet", "CreateCashWallet", map[string]any{"hub_app_id": 9, "recipients": []any{map[string]any{"identity_type": "pubkey", "identity_value": tPub, "amount_mloki": 1000}}},
			[]map[string]any{
				{"hub_app_id": 9, "recipients": []any{map[string]any{"identity_type": "cash", "identity_value": "", "amount_mloki": 1000}}},
				{"hub_app_id": 9, "recipients": []any{map[string]any{"identity_type": "pubkey", "identity_value": tPub, "amount_mloki": 0}}},
				{"hub_app_id": 9, "recipients": []any{map[string]any{"identity_type": "connection_key", "identity_value": tPub, "amount_mloki": 10}}},
				{"hub_app_id": 9, "recipients": []any{}},
				{"hub_app_id": 8, "recipients": []any{map[string]any{"identity_type": "pubkey", "identity_value": tPub, "amount_mloki": 1000}}},
			}},
		{"put_order_terms", "PutOrderTerms", map[string]any{"app_id": 9, "settings": terms},
			[]map[string]any{{"app_id": 0, "settings": terms}, {"app_id": 8, "settings": terms}}},
	}

	s, rec := newDeferred()
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
			if rec.n(c.method) != 0 {
				t.Fatalf("%s executed on the first call", c.method)
			}
			args["confirm_code"] = st.ConfirmCode
			r = callTool(t, cs, c.tool, args)
			if r.IsError || stepOf(t, r.StructuredContent).Status != "executed" {
				t.Fatalf("confirm failed: %s", resultText(r))
			}
			if out := scJSON(t, r.StructuredContent); strings.Contains(out, "SECRET") {
				t.Errorf("result leaks a secret: %s", out)
			}
			if rec.n(c.method) != 1 {
				t.Fatalf("%s calls = %d, want 1", c.method, rec.n(c.method))
			}
			for j, b := range c.bad {
				ba := map[string]any{"idempotency_key": "bad" + c.tool + string(rune('a'+i)) + string(rune('a'+j))}
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

	// The JIT purchase uses the LSP's LIVE offer, and the preview fee is the
	// hub's computation, not the model's cap.
	if rec.buyParams == nil || rec.buyParams.Promise != "PROMISE-OPAQUE" {
		t.Errorf("LSPS2Buy did not use the live fee offer: %+v", rec.buyParams)
	}
	// An update merges over the listing's current fields.
	var f listing.Fields
	_ = json.Unmarshal(rec.bodies["update"], &f)
	if f.Title != "New" || f.Content != "kept content" || len(f.Languages) != 1 {
		t.Errorf("update body did not merge current fields: %s", rec.bodies["update"])
	}
	if strings.Contains(string(rec.bodies["create"]), "confirm_follow_set_public\":true") {
		t.Error("create self-asserted the follow-set confirmation")
	}
	if rec.action != "pause" {
		t.Errorf("listing action = %q", rec.action)
	}
	if rec.cashReq == nil || len(rec.cashReq.Recipients) != 1 || rec.cashReq.RequestID != "" {
		t.Errorf("cash request = %+v", rec.cashReq)
	}
}

// TestLSPBuyPreviewFeeFromHub: the previewed amount is the fee the hub computed
// from the LSP's live offer.
func TestLSPBuyPreviewFeeFromHub(t *testing.T) {
	s, _ := newDeferred()
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()
	r := callTool(t, cs, "lsp_buy_jit_channel", map[string]any{"idempotency_key": "f", "lsp_pubkey": tLSPNode,
		"payment_size_mloki": 50_000_000, "max_fee_mloki": 1_000_000})
	st := stepOf(t, r.StructuredContent)
	if st.Preview == nil || st.Preview.AmountMloki != 50_000 { // 50,000,000 * 1000ppm = 50,000 > min 10,000
		t.Fatalf("preview fee = %+v, want 50000 mloki", st.Preview)
	}
	if fee, _ := lspsFee(10_000, 1000, 1_000_000); fee != 10_000 {
		t.Errorf("min fee floor not applied: %d", fee)
	}
}

func TestCleanText(t *testing.T) {
	for _, s := range []string{"a‮b", "a\x00", "a b", "‏x", "a\x7f"} {
		if cleanText(s, 100, true) {
			t.Errorf("cleanText accepted %q", s)
		}
	}
	if cleanText("a\nb", 100, false) || !cleanText("a\nb", 100, true) {
		t.Error("newline handling")
	}
}
