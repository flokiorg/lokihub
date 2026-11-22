package mcp

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/flokiorg/lokihub/api"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// testAPI is an api.API double: embedding the interface satisfies all ~118
// methods (an un-overridden call would panic on the nil embed), so a test only
// needs to implement the handful its tools actually invoke.
type testAPI struct {
	api.API
	aliasCalls  *int32 // SetNodeAlias increments (write-dedup tests)
	redeemCalls *int32 // RedeemOnchainFunds increments (residual-guard tests)
}

func (testAPI) GetInfo(context.Context) (*api.InfoResponse, error) { return &api.InfoResponse{}, nil }
func (testAPI) GetBalances(context.Context) (*api.BalancesResponse, error) {
	return &api.BalancesResponse{}, nil
}
func (t testAPI) SetNodeAlias(context.Context, string) error {
	if t.aliasCalls != nil {
		atomic.AddInt32(t.aliasCalls, 1)
	}
	return nil
}
func (t testAPI) RedeemOnchainFunds(context.Context, string, uint64, *uint64, bool) (*api.RedeemOnchainFundsResponse, error) {
	if t.redeemCalls != nil {
		atomic.AddInt32(t.redeemCalls, 1)
	}
	return &api.RedeemOnchainFundsResponse{}, nil
}

// connectInMem wires an in-memory MCP client to the server built for conn.
func connectInMem(t *testing.T, s *Server, conn *Conn) (*mcpsdk.ClientSession, func()) {
	t.Helper()
	ctx := context.Background()
	var ct, st mcpsdk.Transport = mcpsdk.NewInMemoryTransports()
	ss, err := s.serverForConn(conn).Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	return cs, func() { cs.Close(); ss.Wait() }
}

// TestEndToEndReadDispatch drives a real in-memory MCP client against the server
// built for one connection: the advertised catalogue is scope-filtered, a
// granted read succeeds, and a tool outside the grant is not reachable. This is
// M1's integration coverage at the Go-test level (no regtest hub needed) and
// the dispatch half of M-SCOPE-1.
func TestEndToEndReadDispatch(t *testing.T) {
	s := NewServer(testAPI{}, &fakeAuth{}) // fakeAuth.Revalidate passes (no dead)
	conn := &Conn{
		AppID:  1,
		Name:   "assistant",
		Scopes: []string{GroupNode.Scope(ModeRead), GroupWallet.Scope(ModeRead)},
	}
	ctx := context.Background()

	var ct, st mcpsdk.Transport = mcpsdk.NewInMemoryTransports()
	server := s.serverForConn(conn)
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { cs.Close(); ss.Wait() }()

	// Catalogue is scope-filtered: Node + Wallet reads present, nothing else.
	lt, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]bool{}
	for _, tl := range lt.Tools {
		got[tl.Name] = true
	}
	for _, want := range []string{"get_info", "health", "node_status", "get_balances", "wallet_capabilities"} {
		if !got[want] {
			t.Errorf("granted tool %q missing from catalogue", want)
		}
	}
	for _, deny := range []string{"list_channels", "list_peers", "list_identity_authorities", "cash_hubs_stats", "get_listing_relays", "list_lsp_catalogue", "list_circle_identities", "list_forwards"} {
		if got[deny] {
			t.Errorf("ungranted tool %q leaked into the catalogue", deny)
		}
	}

	// A granted read dispatches and succeeds.
	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_info"})
	if err != nil {
		t.Fatalf("CallTool get_info: %v", err)
	}
	if res.IsError {
		t.Errorf("get_info returned IsError: %+v", res.Content)
	}

	// A tool outside the grant is not reachable (not advertised; calling by name
	// must not silently succeed).
	res2, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "list_channels"})
	if err == nil && (res2 == nil || !res2.IsError) {
		t.Errorf("ungranted tool list_channels was not refused (err=%v res=%+v)", err, res2)
	}
}
