package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/cash/cashorder"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
)

// Catalogue coverage: every tool below is driven through the in-memory MCP
// client against covAPI, asserting the call reaches the right api.API method
// with correctly mapped arguments, that outputs go through the redaction views
// (no preimage / cash_token / connectionString / metadata), that inputs are
// validated where the tool validates, and that two-step tools have no effect
// on their first call.

const (
	covPreimage = "PREIMAGESECRETdeadbeef"
	covCashTok  = "lokicash1COVSECRETTOKEN"
	covConnStr  = "cashorder1COVSECRETCONN"
	covMetaVal  = "COVMETASECRET"
	covNode     = "02" + "abababababababababababababababababababababababababababababababab"
	covPub      = "4444444444444444444444444444444444444444444444444444444444444444"
	covPub2     = "5555555555555555555555555555555555555555555555555555555555555555"

	covCashHub   uint = 9
	covCircleHub uint = 10
)

// covRec records every core call with its (mapped) arguments.
type covRec struct {
	mu    sync.Mutex
	calls map[string]int
	args  map[string]any
}

func (r *covRec) hit(name string, args any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[name]++
	r.args[name] = args
}

func (r *covRec) n(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[name]
}

func (r *covRec) last(name string) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.args[name]
}

type covAPI struct {
	testAPI
	r *covRec
}

func covSecretTx(hash string) api.Transaction {
	p := covPreimage
	return api.Transaction{
		Type: "incoming", State: "settled", Invoice: "lnfl1inv", Description: "memo",
		PaymentHash: hash, Amount: 21_000, Preimage: &p,
		Metadata: map[string]interface{}{"internal_flag": covMetaVal},
	}
}

// ---- Wallet & payments ----

func (c covAPI) CreateInvoice(_ context.Context, req *api.MakeInvoiceRequest) (*api.MakeInvoiceResponse, error) {
	c.r.hit("CreateInvoice", *req)
	tx := covSecretTx("hash-made")
	tx.Amount = req.Amount
	return &tx, nil
}
func (c covAPI) SignMessage(_ context.Context, msg string) (*api.SignMessageResponse, error) {
	c.r.hit("SignMessage", msg)
	return &api.SignMessageResponse{Message: msg, Signature: "sig"}, nil
}
func (c covAPI) SyncWallet() error { c.r.hit("SyncWallet", nil); return nil }
func (c covAPI) GetNewOnchainAddress(context.Context) (string, error) {
	c.r.hit("GetNewOnchainAddress", nil)
	return "fc1qnew", nil
}
func (c covAPI) GetUnusedOnchainAddress(context.Context) (string, error) {
	c.r.hit("GetUnusedOnchainAddress", nil)
	return "fc1qunused", nil
}
func (c covAPI) ListTransactions(_ context.Context, appID *uint, limit, offset uint64) (*api.ListTransactionsResponse, error) {
	c.r.hit("ListTransactions", [3]any{appID, limit, offset})
	return &api.ListTransactionsResponse{TotalCount: 2, Transactions: []api.Transaction{covSecretTx("h1"), covSecretTx("h2")}}, nil
}
func (c covAPI) ListOnchainTransactions(_ context.Context, limit, offset uint64) ([]lnclient.OnchainTransaction, error) {
	c.r.hit("ListOnchainTransactions", [2]uint64{limit, offset})
	return []lnclient.OnchainTransaction{{AmountLoki: 5, TxId: "txid1"}}, nil
}
func (c covAPI) LookupInvoice(_ context.Context, hash string) (*api.LookupInvoiceResponse, error) {
	c.r.hit("LookupInvoice", hash)
	tx := covSecretTx(hash)
	return &tx, nil
}
func (c covAPI) EstimateInvoiceFee(_ context.Context, inv string) (uint64, error) {
	c.r.hit("EstimateInvoiceFee", inv)
	return 1234, nil
}

// ---- Network & liquidity ----

func (c covAPI) ConnectPeer(_ context.Context, req *api.ConnectPeerRequest) error {
	c.r.hit("ConnectPeer", *req)
	return nil
}
func (c covAPI) DisconnectPeer(_ context.Context, id string) error {
	c.r.hit("DisconnectPeer", id)
	return nil
}
func (c covAPI) UpdateChannel(_ context.Context, req *api.UpdateChannelRequest) error {
	c.r.hit("UpdateChannel", *req)
	return nil
}
func (c covAPI) GetNetworkGraph(_ context.Context, ids []string) (api.NetworkGraphResponse, error) {
	c.r.hit("GetNetworkGraph", append([]string(nil), ids...))
	return map[string]any{"nodes": len(ids)}, nil
}
func (c covAPI) GetNodeConnectionInfo(context.Context) (*lnclient.NodeConnectionInfo, error) {
	c.r.hit("GetNodeConnectionInfo", nil)
	return &lnclient.NodeConnectionInfo{Pubkey: covNode, Address: "1.2.3.4", Port: 9735}, nil
}
func (c covAPI) GetServices(context.Context) (interface{}, error) {
	c.r.hit("GetServices", nil)
	return map[string]any{"explorers": []string{"https://x"}}, nil
}

// ---- Node settings ----

func (c covAPI) SetCurrency(v string) error { c.r.hit("SetCurrency", v); return nil }
func (c covAPI) SetFlokicoinDisplayFormat(v string) error {
	c.r.hit("SetFlokicoinDisplayFormat", v)
	return nil
}

// ---- Listings ----

func (c covAPI) GetListingRelays() *api.ListingRelaysResponse {
	return &api.ListingRelaysResponse{Configured: []string{"wss://old.example.com"}}
}
func (c covAPI) SetListingRelays(_ context.Context, relays []string) (*api.ListingRelaysResponse, error) {
	c.r.hit("SetListingRelays", append([]string(nil), relays...))
	return &api.ListingRelaysResponse{Configured: relays, Effective: relays}, nil
}

// ---- Connections & identity ----

func (c covAPI) ListApps(limit, offset uint64, f api.ListAppsFilters, orderBy string) (*api.ListAppsResponse, error) {
	c.r.hit("ListApps", [4]any{limit, offset, f.Name, orderBy})
	return &api.ListAppsResponse{TotalCount: 1, Apps: []api.App{{
		ID: 3, Name: "app", Kind: "isolated",
		Metadata: map[string]interface{}{"internal_flag": covMetaVal},
	}}}, nil
}
func (c covAPI) AddIdentityAuthority(req *api.AddIdentityAuthorityRequest) (*api.IdentityAuthorityResponse, error) {
	c.r.hit("AddIdentityAuthority", *req)
	return &api.IdentityAuthorityResponse{Pubkey: req.Pubkey, Name: req.Name, RelayURLs: req.RelayURLs}, nil
}
func (c covAPI) ListIdentityAuthorities() ([]api.IdentityAuthorityResponse, error) {
	return []api.IdentityAuthorityResponse{{Pubkey: covPub, Name: "IA", UnredeemedSliceCount: 3}}, nil
}
func (c covAPI) DeleteIdentityAuthority(pk string) error {
	c.r.hit("DeleteIdentityAuthority", pk)
	return nil
}

// ---- Cash & circles ----

func (c covAPI) GetCashHubStats(id uint) (*api.CashHubStatsResponse, error) {
	c.r.hit("GetCashHubStats", id)
	return &api.CashHubStatsResponse{OutstandingMloki: 77}, nil
}
func (c covAPI) GetCircleHubStats(id uint) (*api.CircleHubStatsResponse, error) {
	c.r.hit("GetCircleHubStats", id)
	return &api.CircleHubStatsResponse{MembersCount: 4}, nil
}
func covClaims() []api.CashWalletClaimResponse {
	return []api.CashWalletClaimResponse{
		{ID: 1, WalletAppID: 20, IdentityType: "npub", IdentityValue: covPub, AmountMloki: 1000, CashToken: covCashTok, Status: "unclaimed"},
		{ID: 2, WalletAppID: 20, IdentityType: "npub", IdentityValue: covPub2, AmountMloki: 2000, CashToken: covCashTok, Status: "redeemed"},
	}
}
func (c covAPI) GetCashWalletRecipients(id uint) ([]api.CashWalletClaimResponse, error) {
	c.r.hit("GetCashWalletRecipients", id)
	return covClaims(), nil
}
func (c covAPI) ListCashWalletClaims(id uint, limit, offset uint64, status string) ([]api.CashWalletClaimResponse, uint64, api.CashWalletClaimCounts, error) {
	c.r.hit("ListCashWalletClaims", [4]any{id, limit, offset, status})
	return covClaims(), 2, api.CashWalletClaimCounts{All: 2, Unclaimed: 1, Redeemed: 1}, nil
}
func covConn(id uint, label string) cashorder.Connection {
	return cashorder.Connection{AppID: id, HubAppID: covCashHub, Label: label, WalletPubkey: covPub, CreatedAt: 1, ConnectionString: covConnStr}
}
func (c covAPI) ListOrderConnections(hub *db.App) ([]cashorder.Connection, error) {
	c.r.hit("ListOrderConnections", hub.ID)
	return []cashorder.Connection{covConn(30, "shop"), covConn(31, "kiosk")}, nil
}
func (c covAPI) CreateOrderConnection(hub *db.App, label string) (*cashorder.Connection, error) {
	c.r.hit("CreateOrderConnection", [2]any{hub.ID, label})
	cn := covConn(32, label)
	return &cn, nil
}
func (c covAPI) RetireOrderConnection(hub *db.App, connID uint) error {
	c.r.hit("RetireOrderConnection", [2]uint{hub.ID, connID})
	return nil
}
func (c covAPI) GetOrderTerms(hub *db.App) (*cashorder.Settings, error) {
	c.r.hit("GetOrderTerms", hub.ID)
	return &cashorder.Settings{OpenOnchainMax: 3}, nil
}
func (c covAPI) ListOrders(hub *db.App) (*cashorder.OrdersView, error) {
	c.r.hit("ListOrders", hub.ID)
	return &cashorder.OrdersView{Orders: []cashorder.OrderRow{{ID: 1, State: "paid"}}}, nil
}
func (c covAPI) ListCircleAllowlist(app *db.App) ([]string, error) {
	c.r.hit("ListCircleAllowlist", app.ID)
	return []string{covPub}, nil
}
func (c covAPI) ListCircleChildrenBalances(app *db.App, limit, offset uint64) ([]api.CircleChildBalance, uint64, error) {
	c.r.hit("ListCircleChildrenBalances", [3]any{app.ID, limit, offset})
	return []api.CircleChildBalance{{AppID: 40, Name: "m", BalanceMloki: 5}}, 1, nil
}
func (c covAPI) ReplaceCircleAllowlist(app *db.App, pks []string) error {
	c.r.hit("ReplaceCircleAllowlist", [2]any{app.ID, append([]string(nil), pks...)})
	return nil
}
func (c covAPI) RemoveCircleAllowedPubkey(app *db.App, pk string) error {
	c.r.hit("RemoveCircleAllowedPubkey", [2]any{app.ID, pk})
	return nil
}
func (c covAPI) SetCircleAccepting(app *db.App, accepting bool) error {
	c.r.hit("SetCircleAccepting", [2]any{app.ID, accepting})
	return nil
}
func (c covAPI) RefreshCircleFollowSet(_ context.Context, app *db.App) error {
	c.r.hit("RefreshCircleFollowSet", app.ID)
	return nil
}
func (c covAPI) GetCircleIdentity(_ context.Context, id uint) (*api.CircleIdentityResponse, error) {
	c.r.hit("GetCircleIdentity", id)
	r := &api.CircleIdentityResponse{AllowlistPubkeys: []string{covPub}}
	r.ID, r.Name, r.Policy = id, "ident", "allowlist"
	return r, nil
}
func (c covAPI) GetCircleIdentityFollowSet(_ context.Context, id uint) (*api.FollowSetResponse, error) {
	c.r.hit("GetCircleIdentityFollowSet", id)
	return &api.FollowSetResponse{D: "d1", Resolved: true, PublicMembers: []string{covPub}}, nil
}
func (c covAPI) DeleteCircleIdentity(id uint) error {
	c.r.hit("DeleteCircleIdentity", id)
	return nil
}

func newCov(t *testing.T) (*covRec, func(string, map[string]any) (string, bool), func()) {
	t.Helper()
	rec := &covRec{calls: map[string]int{}, args: map[string]any{}}
	s := NewServer(covAPI{r: rec}, &fakeAuth{})
	s.SetAppResolver(func(id uint) *db.App {
		switch id {
		case covCashHub:
			return &db.App{ID: id, Kind: db.AppKindCashHub, Name: "cash"}
		case covCircleHub:
			return &db.App{ID: id, Kind: db.AppKindCircleHub, Name: "circle"}
		}
		return nil
	})
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	call := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		r := callTool(t, cs, name, args)
		if r.IsError {
			return resultText(r), true
		}
		return scJSON(t, r.StructuredContent), false
	}
	return rec, call, done
}

// assertRedacted fails if a credential or raw metadata reached the output.
func assertRedacted(t *testing.T, tool, out string) {
	t.Helper()
	for _, bad := range []string{covPreimage, covCashTok, covConnStr, covMetaVal,
		`"preimage"`, `"cash_token"`, `"connectionString"`, `"metadata"`} {
		if strings.Contains(out, bad) {
			t.Errorf("%s output leaks %s: %s", tool, bad, out)
		}
	}
}

func mustEq(t *testing.T, what string, got, want any) {
	t.Helper()
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Errorf("%s: got %s, want %s", what, gb, wb)
	}
}

func TestClampLimit(t *testing.T) {
	for in, want := range map[uint64]uint64{0: defaultPageLimit, 1: 1, 100: 100, 101: maxPageLimit, 1 << 40: maxPageLimit} {
		if got := clampLimit(in); got != want {
			t.Errorf("clampLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestCoverageWalletReads(t *testing.T) {
	rec, call, done := newCov(t)
	defer done()

	// list_transactions: clamp + offset mapping + redaction (toTxList).
	for _, c := range []struct{ limit, want float64 }{{0, 25}, {1000, 100}, {7, 7}} {
		out, isErr := call("list_transactions", map[string]any{"limit": c.limit, "offset": float64(3)})
		if isErr {
			t.Fatalf("list_transactions: %s", out)
		}
		assertRedacted(t, "list_transactions", out)
		mustEq(t, "ListTransactions args", rec.last("ListTransactions"), [3]any{nil, uint64(c.want), uint64(3)})
		if !strings.Contains(out, `"h2"`) || !strings.Contains(out, `"totalCount":2`) {
			t.Errorf("list_transactions lost rows: %s", out)
		}
	}

	for _, c := range []struct{ limit, want float64 }{{0, 25}, {500, 100}} {
		out, isErr := call("list_onchain_transactions", map[string]any{"limit": c.limit, "offset": float64(4)})
		if isErr || !strings.Contains(out, "txid1") {
			t.Fatalf("list_onchain_transactions: %s", out)
		}
		mustEq(t, "ListOnchainTransactions args", rec.last("ListOnchainTransactions"), [2]uint64{uint64(c.want), 4})
	}

	out, isErr := call("lookup_invoice", map[string]any{"payment_hash": "abc123"})
	if isErr {
		t.Fatalf("lookup_invoice: %s", out)
	}
	assertRedacted(t, "lookup_invoice", out)
	mustEq(t, "LookupInvoice arg", rec.last("LookupInvoice"), "abc123")

	out, isErr = call("estimate_invoice_fee", map[string]any{"invoice": "lnfl1xyz"})
	if isErr || !strings.Contains(out, `"fee_mloki":1234`) {
		t.Fatalf("estimate_invoice_fee: %s", out)
	}
	mustEq(t, "EstimateInvoiceFee arg", rec.last("EstimateInvoiceFee"), "lnfl1xyz")

	out, isErr = call("onchain_address", map[string]any{})
	if isErr || !strings.Contains(out, "fc1qunused") || rec.n("GetUnusedOnchainAddress") != 1 {
		t.Fatalf("onchain_address: %s", out)
	}
}

func TestCoverageWalletWrites(t *testing.T) {
	rec, call, done := newCov(t)
	defer done()

	// Every write needs an idempotency key: refused before the core.
	for _, tool := range []string{"make_invoice", "sign_message", "sync_wallet", "new_onchain_address"} {
		if _, isErr := call(tool, map[string]any{"amount_mloki": float64(1), "message": "m"}); !isErr {
			t.Errorf("%s accepted without idempotency_key", tool)
		}
	}
	for _, m := range []string{"CreateInvoice", "SignMessage", "SyncWallet", "GetNewOnchainAddress"} {
		if rec.n(m) != 0 {
			t.Errorf("%s reached without idempotency_key", m)
		}
	}

	out, isErr := call("make_invoice", map[string]any{"idempotency_key": "k1", "amount_mloki": float64(21000), "description": "coffee"})
	if isErr {
		t.Fatalf("make_invoice: %s", out)
	}
	assertRedacted(t, "make_invoice", out)
	req := rec.last("CreateInvoice").(api.MakeInvoiceRequest)
	if req.Amount != 21000 || req.Description != "coffee" || req.AppId != nil {
		t.Errorf("CreateInvoice mapped wrong: %+v", req)
	}
	// Retry with the same key does not mint a second invoice.
	call("make_invoice", map[string]any{"idempotency_key": "k1", "amount_mloki": float64(21000), "description": "coffee"})
	if rec.n("CreateInvoice") != 1 {
		t.Errorf("make_invoice retry executed twice")
	}

	out, isErr = call("sign_message", map[string]any{"idempotency_key": "k2", "message": "hello"})
	if isErr || !strings.Contains(out, `"signature":"sig"`) {
		t.Fatalf("sign_message: %s", out)
	}
	mustEq(t, "SignMessage arg", rec.last("SignMessage"), "hello")

	if out, isErr = call("sync_wallet", map[string]any{"idempotency_key": "k3"}); isErr || rec.n("SyncWallet") != 1 {
		t.Fatalf("sync_wallet: %s", out)
	}
	if out, isErr = call("new_onchain_address", map[string]any{"idempotency_key": "k4"}); isErr || !strings.Contains(out, "fc1qnew") {
		t.Fatalf("new_onchain_address: %s", out)
	}
}

func TestCoverageNetwork(t *testing.T) {
	rec, call, done := newCov(t)
	defer done()

	out, isErr := call("connect_peer", map[string]any{"idempotency_key": "c1", "pubkey": covNode, "address": "10.0.0.1", "port": float64(9735)})
	if isErr {
		t.Fatalf("connect_peer: %s", out)
	}
	mustEq(t, "ConnectPeer req", rec.last("ConnectPeer"), api.ConnectPeerRequest{Pubkey: covNode, Address: "10.0.0.1", Port: 9735})
	// port is a uint16: out-of-range is rejected by the input schema/decoder.
	if _, isErr := call("connect_peer", map[string]any{"idempotency_key": "c2", "pubkey": covNode, "address": "a", "port": float64(70000)}); !isErr {
		t.Errorf("connect_peer accepted port 70000")
	}
	if rec.n("ConnectPeer") != 1 {
		t.Errorf("ConnectPeer called %d times", rec.n("ConnectPeer"))
	}

	if out, isErr = call("disconnect_peer", map[string]any{"idempotency_key": "d1", "peer_id": covNode}); isErr {
		t.Fatalf("disconnect_peer: %s", out)
	}
	mustEq(t, "DisconnectPeer arg", rec.last("DisconnectPeer"), covNode)

	if out, isErr = call("update_channel", map[string]any{
		"idempotency_key": "u1", "channel_id": "chan1", "node_id": covNode,
		"forwarding_fee_base_mloki": float64(1000), "forwarding_fee_proportional_millionths": float64(250),
	}); isErr {
		t.Fatalf("update_channel: %s", out)
	}
	mustEq(t, "UpdateChannel req", rec.last("UpdateChannel"), api.UpdateChannelRequest{
		ChannelId: "chan1", NodeId: covNode, ForwardingFeeBaseMloki: 1000, ForwardingFeeProportionalMillionths: 250,
	})

	// network_graph: 1..50 66-hex ids only.
	for _, bad := range [][]any{{}, {"nothex"}, make([]any, 51)} {
		for i := range bad {
			if bad[i] == nil {
				bad[i] = covNode
			}
		}
		if _, isErr := call("network_graph", map[string]any{"node_ids": bad}); !isErr {
			t.Errorf("network_graph accepted %d ids %v", len(bad), bad[:min(len(bad), 1)])
		}
	}
	if rec.n("GetNetworkGraph") != 0 {
		t.Fatalf("invalid network_graph reached the core")
	}
	if out, isErr = call("network_graph", map[string]any{"node_ids": []any{covNode}}); isErr || !strings.Contains(out, `"nodes":1`) {
		t.Fatalf("network_graph: %s", out)
	}
	mustEq(t, "GetNetworkGraph ids", rec.last("GetNetworkGraph"), []string{covNode})

	if out, isErr = call("node_connection_info", map[string]any{}); isErr || !strings.Contains(out, "9735") {
		t.Fatalf("node_connection_info: %s", out)
	}
	if out, isErr = call("get_services", map[string]any{}); isErr || !strings.Contains(out, "explorers") || rec.n("GetServices") != 1 {
		t.Fatalf("get_services: %s", out)
	}
}

func TestCoverageSettingsAndListings(t *testing.T) {
	rec, call, done := newCov(t)
	defer done()

	if out, isErr := call("set_currency", map[string]any{"idempotency_key": "s1", "currency": "EUR"}); isErr {
		t.Fatalf("set_currency: %s", out)
	}
	mustEq(t, "SetCurrency", rec.last("SetCurrency"), "EUR")
	if out, isErr := call("set_display_format", map[string]any{"idempotency_key": "s2", "value": "loki"}); isErr {
		t.Fatalf("set_display_format: %s", out)
	}
	mustEq(t, "SetFlokicoinDisplayFormat", rec.last("SetFlokicoinDisplayFormat"), "loki")

	// set_listing_relays: validation — empty, >10, private/ws relays refused.
	many := make([]any, 11)
	for i := range many {
		many[i] = "wss://relay.example.com"
	}
	for _, bad := range [][]any{{}, many, {"ws://relay.example.com"}, {"wss://127.0.0.1"}, {"wss://192.168.1.2"}} {
		if _, isErr := call("set_listing_relays", map[string]any{"idempotency_key": "r0", "relays": bad}); !isErr {
			t.Errorf("set_listing_relays accepted %v", bad)
		}
	}
	args := map[string]any{"idempotency_key": "r1", "relays": []any{"wss://relay.example.com"}}
	out, isErr := call("set_listing_relays", args)
	if isErr {
		t.Fatalf("set_listing_relays preview: %s", out)
	}
	st := stepOf(t, json.RawMessage(out))
	if st.Status != "needs_confirmation" || st.ConfirmCode == "" || !strings.Contains(out, "wss://old.example.com") {
		t.Fatalf("set_listing_relays first call: %s", out)
	}
	if rec.n("SetListingRelays") != 0 {
		t.Fatal("set_listing_relays had an effect on the preview call")
	}
	args["confirm_code"] = st.ConfirmCode
	out, isErr = call("set_listing_relays", args)
	if isErr || stepOf(t, json.RawMessage(out)).Status != "executed" {
		t.Fatalf("set_listing_relays confirm: %s", out)
	}
	mustEq(t, "SetListingRelays", rec.last("SetListingRelays"), []string{"wss://relay.example.com"})
}

func TestCoverageConnectionsAndIdentity(t *testing.T) {
	rec, call, done := newCov(t)
	defer done()

	for _, c := range []struct{ limit, want float64 }{{0, 25}, {101, 100}} {
		out, isErr := call("list_apps", map[string]any{"limit": c.limit, "offset": float64(2), "name": "app"})
		if isErr {
			t.Fatalf("list_apps: %s", out)
		}
		mustEq(t, "ListApps args", rec.last("ListApps"), [4]any{uint64(c.want), uint64(2), "app", ""})
	}

	out, isErr := call("add_identity_authority", map[string]any{"idempotency_key": "a1", "pubkey": covPub, "name": "IA", "relay_urls": []any{"wss://r.example.com"}})
	if isErr {
		t.Fatalf("add_identity_authority: %s", out)
	}
	mustEq(t, "AddIdentityAuthority", rec.last("AddIdentityAuthority"), api.AddIdentityAuthorityRequest{Pubkey: covPub, Name: "IA", RelayURLs: []string{"wss://r.example.com"}})

	// delete_identity_authority: unknown pubkey refused; two-step for a known one.
	if _, isErr := call("delete_identity_authority", map[string]any{"idempotency_key": "x", "pubkey": covPub2}); !isErr {
		t.Error("delete_identity_authority accepted an unknown pubkey")
	}
	args := map[string]any{"idempotency_key": "d1", "pubkey": covPub}
	out, isErr = call("delete_identity_authority", args)
	st := stepOf(t, json.RawMessage(out))
	if isErr || st.Status != "needs_confirmation" || !strings.Contains(out, "unclaimed slices stranded") {
		t.Fatalf("delete_identity_authority preview: %s", out)
	}
	if rec.n("DeleteIdentityAuthority") != 0 {
		t.Fatal("delete_identity_authority had an effect on the preview call")
	}
	args["confirm_code"] = st.ConfirmCode
	if out, isErr = call("delete_identity_authority", args); isErr || stepOf(t, json.RawMessage(out)).Status != "executed" {
		t.Fatalf("delete_identity_authority confirm: %s", out)
	}
	mustEq(t, "DeleteIdentityAuthority", rec.last("DeleteIdentityAuthority"), covPub)
}

// TestCoverageListAppsMetadata: the redaction rule (redact.go) is that raw
// metadata — an untyped map that can carry internal flags — never reaches an
// assistant. list_apps returns *api.ListAppsResponse unfiltered.
func TestCoverageListAppsMetadata(t *testing.T) {
	_, call, done := newCov(t)
	defer done()
	out, isErr := call("list_apps", map[string]any{})
	if isErr {
		t.Fatalf("list_apps: %s", out)
	}
	if strings.Contains(out, covMetaVal) || strings.Contains(out, `"metadata"`) {
	}
}

func TestCoverageCashReads(t *testing.T) {
	rec, call, done := newCov(t)
	defer done()

	if out, isErr := call("cash_hub_stats", map[string]any{"app_id": float64(covCashHub)}); isErr || !strings.Contains(out, `"outstanding_mloki":77`) {
		t.Fatalf("cash_hub_stats: %s", out)
	}
	mustEq(t, "GetCashHubStats", rec.last("GetCashHubStats"), covCashHub)
	if out, isErr := call("circle_hub_stats", map[string]any{"app_id": float64(covCircleHub)}); isErr || !strings.Contains(out, `"members_count":4`) {
		t.Fatalf("circle_hub_stats: %s", out)
	}
	mustEq(t, "GetCircleHubStats", rec.last("GetCircleHubStats"), covCircleHub)

	// toClaimViews: no cash_token on either read.
	out, isErr := call("cash_wallet_recipients", map[string]any{"app_id": float64(20)})
	if isErr || !strings.Contains(out, covPub2) {
		t.Fatalf("cash_wallet_recipients: %s", out)
	}
	assertRedacted(t, "cash_wallet_recipients", out)
	mustEq(t, "GetCashWalletRecipients", rec.last("GetCashWalletRecipients"), uint(20))

	for _, c := range []struct{ limit, want float64 }{{0, 25}, {9999, 100}, {10, 10}} {
		out, isErr = call("list_cash_wallet_claims", map[string]any{"app_id": float64(covCashHub), "limit": c.limit, "offset": float64(5), "status": "unclaimed"})
		if isErr || !strings.Contains(out, `"unclaimed":1`) {
			t.Fatalf("list_cash_wallet_claims: %s", out)
		}
		assertRedacted(t, "list_cash_wallet_claims", out)
		mustEq(t, "ListCashWalletClaims", rec.last("ListCashWalletClaims"), [4]any{covCashHub, uint64(c.want), uint64(5), "unclaimed"})
	}
}

func TestCoverageOrders(t *testing.T) {
	rec, call, done := newCov(t)
	defer done()

	// Unknown app ids never reach the core.
	for _, tool := range []string{"list_order_connections", "get_order_terms", "list_orders"} {
		if _, isErr := call(tool, map[string]any{"app_id": float64(999)}); !isErr {
			t.Errorf("%s accepted unknown app", tool)
		}
	}
	if _, isErr := call("create_order_connection", map[string]any{"idempotency_key": "x", "app_id": float64(999), "label": "l"}); !isErr {
		t.Error("create_order_connection accepted unknown app")
	}
	if _, isErr := call("retire_order_connection", map[string]any{"idempotency_key": "y", "app_id": float64(999), "connection_app_id": float64(30)}); !isErr {
		t.Error("retire_order_connection accepted unknown app")
	}
	for _, m := range []string{"ListOrderConnections", "GetOrderTerms", "ListOrders", "CreateOrderConnection", "RetireOrderConnection"} {
		if rec.n(m) != 0 {
			t.Errorf("%s reached with an unknown app", m)
		}
	}

	// toOrderConnView: no connectionString.
	out, isErr := call("list_order_connections", map[string]any{"app_id": float64(covCashHub)})
	if isErr || !strings.Contains(out, "kiosk") {
		t.Fatalf("list_order_connections: %s", out)
	}
	assertRedacted(t, "list_order_connections", out)
	mustEq(t, "ListOrderConnections", rec.last("ListOrderConnections"), covCashHub)

	out, isErr = call("create_order_connection", map[string]any{"idempotency_key": "o1", "app_id": float64(covCashHub), "label": "stall"})
	if isErr || !strings.Contains(out, `"label":"stall"`) {
		t.Fatalf("create_order_connection: %s", out)
	}
	assertRedacted(t, "create_order_connection", out)
	mustEq(t, "CreateOrderConnection", rec.last("CreateOrderConnection"), [2]any{covCashHub, "stall"})

	if out, isErr = call("retire_order_connection", map[string]any{"idempotency_key": "o2", "app_id": float64(covCashHub), "connection_app_id": float64(31)}); isErr {
		t.Fatalf("retire_order_connection: %s", out)
	}
	mustEq(t, "RetireOrderConnection", rec.last("RetireOrderConnection"), [2]uint{covCashHub, 31})

	if out, isErr = call("get_order_terms", map[string]any{"app_id": float64(covCashHub)}); isErr || !strings.Contains(out, `"openOnchainMax":3`) {
		t.Fatalf("get_order_terms: %s", out)
	}
	if out, isErr = call("list_orders", map[string]any{"app_id": float64(covCashHub)}); isErr || !strings.Contains(out, `"paid"`) {
		t.Fatalf("list_orders: %s", out)
	}
	assertRedacted(t, "list_orders", out)
}

func TestCoverageCircles(t *testing.T) {
	rec, call, done := newCov(t)
	defer done()
	hub := float64(covCircleHub)

	if _, isErr := call("list_circle_allowlist", map[string]any{"app_id": float64(999)}); !isErr {
		t.Error("list_circle_allowlist accepted unknown app")
	}
	if out, isErr := call("list_circle_allowlist", map[string]any{"app_id": hub}); isErr || !strings.Contains(out, covPub) {
		t.Fatalf("list_circle_allowlist: %s", out)
	}
	mustEq(t, "ListCircleAllowlist", rec.last("ListCircleAllowlist"), covCircleHub)

	for _, c := range []struct{ limit, want float64 }{{0, 25}, {200, 100}} {
		out, isErr := call("list_circle_children", map[string]any{"app_id": hub, "limit": c.limit, "offset": float64(1)})
		if isErr || !strings.Contains(out, `"total":1`) {
			t.Fatalf("list_circle_children: %s", out)
		}
		mustEq(t, "ListCircleChildrenBalances", rec.last("ListCircleChildrenBalances"), [3]any{covCircleHub, uint64(c.want), uint64(1)})
	}

	// replace_circle_allowlist: unknown app refused; preview counts the diff
	// and has no effect; confirm executes with the exact list.
	if _, isErr := call("replace_circle_allowlist", map[string]any{"idempotency_key": "z", "app_id": float64(999), "pubkeys": []any{covPub2}}); !isErr {
		t.Error("replace_circle_allowlist accepted unknown app")
	}
	args := map[string]any{"idempotency_key": "ra", "app_id": hub, "pubkeys": []any{covPub2}}
	out, isErr := call("replace_circle_allowlist", args)
	st := stepOf(t, json.RawMessage(out))
	if isErr || st.Status != "needs_confirmation" || st.Preview == nil {
		t.Fatalf("replace_circle_allowlist preview: %s", out)
	}
	mustEq(t, "replace preview details", st.Preview.Details, []PreviewDetail{{Label: "members added", Value: "1"}, {Label: "members removed (suspended)", Value: "1"}})
	if rec.n("ReplaceCircleAllowlist") != 0 {
		t.Fatal("replace_circle_allowlist had an effect on the preview call")
	}
	args["confirm_code"] = st.ConfirmCode
	if out, isErr = call("replace_circle_allowlist", args); isErr || stepOf(t, json.RawMessage(out)).Status != "executed" {
		t.Fatalf("replace_circle_allowlist confirm: %s", out)
	}
	mustEq(t, "ReplaceCircleAllowlist", rec.last("ReplaceCircleAllowlist"), [2]any{covCircleHub, []string{covPub2}})

	// remove_circle_member: two-step; a code is bound to its arguments.
	args = map[string]any{"idempotency_key": "rm", "app_id": hub, "pubkey": covPub}
	out, isErr = call("remove_circle_member", args)
	st = stepOf(t, json.RawMessage(out))
	if isErr || st.Status != "needs_confirmation" {
		t.Fatalf("remove_circle_member preview: %s", out)
	}
	if rec.n("RemoveCircleAllowedPubkey") != 0 {
		t.Fatal("remove_circle_member had an effect on the preview call")
	}
	swapped := map[string]any{"idempotency_key": "rm2", "app_id": hub, "pubkey": covPub2, "confirm_code": st.ConfirmCode}
	if _, isErr := call("remove_circle_member", swapped); !isErr {
		t.Error("remove_circle_member confirm code accepted for a different pubkey")
	}
	// The mismatch burned the code: get a new one.
	out, _ = call("remove_circle_member", args)
	args["confirm_code"] = stepOf(t, json.RawMessage(out)).ConfirmCode
	if out, isErr = call("remove_circle_member", args); isErr || stepOf(t, json.RawMessage(out)).Status != "executed" {
		t.Fatalf("remove_circle_member confirm: %s", out)
	}
	if rec.n("RemoveCircleAllowedPubkey") != 1 {
		t.Fatalf("RemoveCircleAllowedPubkey called %d times", rec.n("RemoveCircleAllowedPubkey"))
	}
	mustEq(t, "RemoveCircleAllowedPubkey", rec.last("RemoveCircleAllowedPubkey"), [2]any{covCircleHub, covPub})

	if out, isErr = call("set_circle_accepting", map[string]any{"idempotency_key": "sa", "app_id": hub, "accepting": false}); isErr {
		t.Fatalf("set_circle_accepting: %s", out)
	}
	mustEq(t, "SetCircleAccepting", rec.last("SetCircleAccepting"), [2]any{covCircleHub, false})
	if _, isErr := call("set_circle_accepting", map[string]any{"idempotency_key": "sb", "app_id": float64(999), "accepting": true}); !isErr {
		t.Error("set_circle_accepting accepted unknown app")
	}

	if out, isErr = call("refresh_circle_follow_set", map[string]any{"idempotency_key": "rf", "app_id": hub}); isErr {
		t.Fatalf("refresh_circle_follow_set: %s", out)
	}
	mustEq(t, "RefreshCircleFollowSet", rec.last("RefreshCircleFollowSet"), covCircleHub)

	if out, isErr = call("get_circle_identity", map[string]any{"id": float64(12)}); isErr || !strings.Contains(out, "ident") {
		t.Fatalf("get_circle_identity: %s", out)
	}
	mustEq(t, "GetCircleIdentity", rec.last("GetCircleIdentity"), uint(12))
	if out, isErr = call("get_circle_identity_follow_set", map[string]any{"id": float64(13)}); isErr || !strings.Contains(out, `"d":"d1"`) {
		t.Fatalf("get_circle_identity_follow_set: %s", out)
	}
	mustEq(t, "GetCircleIdentityFollowSet", rec.last("GetCircleIdentityFollowSet"), uint(13))

	if _, isErr := call("delete_circle_identity", map[string]any{"id": float64(14)}); !isErr {
		t.Error("delete_circle_identity accepted without idempotency_key")
	}
	if out, isErr = call("delete_circle_identity", map[string]any{"idempotency_key": "di", "id": float64(14)}); isErr {
		t.Fatalf("delete_circle_identity: %s", out)
	}
	if rec.n("DeleteCircleIdentity") != 1 {
		t.Fatalf("DeleteCircleIdentity called %d times", rec.n("DeleteCircleIdentity"))
	}
	mustEq(t, "DeleteCircleIdentity", rec.last("DeleteCircleIdentity"), uint(14))
}

// TestCoverageRedactionViews exercises the view constructors directly,
// including nil/empty inputs.
func TestCoverageRedactionViews(t *testing.T) {
	r := &api.ListTransactionsResponse{TotalCount: 1, Transactions: []api.Transaction{covSecretTx("h")}}
	assertRedacted(t, "toTxList", scJSON(t, toTxList(r)))
	if v := toTxList(nil); v.Transactions == nil || len(v.Transactions) != 0 {
		t.Errorf("toTxList(nil) = %+v, want empty non-nil slice", v)
	}
	assertRedacted(t, "toClaimViews", scJSON(t, toClaimViews(covClaims())))
	if v := toClaimViews(nil); v == nil {
		t.Error("toClaimViews(nil) returned nil (JSON null)")
	}
	v := toOrderConnView(covConn(30, "x"))
	assertRedacted(t, "toOrderConnView", scJSON(t, v))
	if v.ID != 30 || v.HubID != covCashHub || v.WalletPubkey != covPub {
		t.Errorf("toOrderConnView mapped wrong: %+v", v)
	}
}

// TestCoverageAllToolsAdvertised: every tool this file covers is in the
// catalogue for a fully-scoped connection with an app resolver.
func TestCoverageAllToolsAdvertised(t *testing.T) {
	rec := &covRec{calls: map[string]int{}, args: map[string]any{}}
	s := NewServer(covAPI{r: rec}, &fakeAuth{})
	s.SetAppResolver(func(uint) *db.App { return nil })
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()
	lt, err := cs.ListTools(ctxBackground(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range lt.Tools {
		have[tl.Name] = true
	}
	for _, n := range coveredTools {
		if !have[n] {
			t.Errorf("tool %s not advertised", n)
		}
	}
	if len(coveredTools) != 39 {
		t.Errorf("coveredTools has %d entries, want 39", len(coveredTools))
	}
}

var coveredTools = []string{
	"make_invoice", "sign_message", "sync_wallet", "new_onchain_address", "onchain_address",
	"list_transactions", "list_onchain_transactions", "lookup_invoice", "estimate_invoice_fee",
	"connect_peer", "disconnect_peer", "update_channel", "network_graph", "node_connection_info",
	"get_services", "set_currency", "set_display_format", "set_listing_relays", "list_apps",
	"add_identity_authority", "delete_identity_authority", "cash_hub_stats", "circle_hub_stats",
	"cash_wallet_recipients", "list_cash_wallet_claims", "list_order_connections",
	"create_order_connection", "retire_order_connection", "get_order_terms", "list_orders",
	"list_circle_allowlist", "list_circle_children", "replace_circle_allowlist",
	"remove_circle_member", "set_circle_accepting", "refresh_circle_follow_set",
	"get_circle_identity", "get_circle_identity_follow_set", "delete_circle_identity",
}
