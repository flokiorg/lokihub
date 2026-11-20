package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAuth is a test Authenticator: tokens map to connections, and an app id in
// dead{} is treated as revoked/expired.
type fakeAuth struct {
	byToken  map[string]*Conn
	dead     map[uint]bool
	rescoped map[uint][]string // owner narrowed/widened scopes since Resolve
}

func (f *fakeAuth) Resolve(_ context.Context, bearer string) (*Conn, error) {
	c, ok := f.byToken[bearer]
	if !ok {
		return nil, ErrInvalidToken
	}
	if f.dead[c.AppID] {
		return nil, ErrRevoked
	}
	return c, nil
}

func (f *fakeAuth) Revalidate(_ context.Context, c *Conn) ([]string, error) {
	if f.dead[c.AppID] {
		return nil, ErrRevoked
	}
	if f.rescoped != nil {
		if sc, ok := f.rescoped[c.AppID]; ok {
			return sc, nil
		}
	}
	return c.Scopes, nil
}

func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc123":  "abc123",
		"bearer abc123":  "abc123", // case-insensitive scheme
		"Bearer  spaced": "spaced",
		"Basic abc":      "",
		"":               "",
		"Bearer":         "",
	}
	for in, want := range cases {
		if got := bearerToken(in); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLoopbackBindsLocalhostOnly is M-BIND-1 (blackbox): the desktop listener
// must bind 127.0.0.1 (never 0.0.0.0) and must 401 without a valid token,
// leaking no catalogue detail.
func TestLoopbackBindsLocalhostOnly(t *testing.T) {
	auth := &fakeAuth{
		byToken: map[string]*Conn{
			"good": {AppID: 1, Name: "t", Scopes: []string{GroupNode.Scope(ModeRead)}},
		},
	}
	s := NewServer(nil, auth) // no api calls are made on these paths
	srv, addr, err := s.ListenLoopback(0)
	if err != nil {
		t.Fatalf("ListenLoopback: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	if !strings.HasPrefix(addr.String(), "127.0.0.1:") {
		t.Fatalf("listener bound to %s, expected 127.0.0.1 only", addr.String())
	}
	base := "http://" + addr.String() + "/mcp"

	// No token → 401, body leaks nothing.
	if code, body := do(t, http.MethodPost, base, ""); code != http.StatusUnauthorized {
		t.Errorf("no-token POST = %d, want 401", code)
	} else if strings.Contains(strings.ToLower(body), "tool") {
		t.Errorf("401 body leaked catalogue detail: %q", body)
	}

	// Unknown token → 401.
	if code, _ := do(t, http.MethodPost, base, "nope"); code != http.StatusUnauthorized {
		t.Errorf("bad-token POST = %d, want 401", code)
	}

	// Valid token → auth passes (not 401; the SDK then handles the request).
	if code, _ := do(t, http.MethodPost, base, "good"); code == http.StatusUnauthorized {
		t.Errorf("valid-token POST = 401, auth should have passed")
	}
}

// TestRevokedTokenRejected: a token whose app is revoked is refused at Resolve
// (per-request), so revocation takes effect on the next call.
func TestRevokedTokenRejected(t *testing.T) {
	auth := &fakeAuth{
		byToken: map[string]*Conn{"good": {AppID: 1, Scopes: []string{GroupNode.Scope(ModeRead)}}},
		dead:    map[uint]bool{1: true},
	}
	s := NewServer(nil, auth)
	srv, addr, err := s.ListenLoopback(0)
	if err != nil {
		t.Fatalf("ListenLoopback: %v", err)
	}
	defer srv.Close()
	if code, _ := do(t, http.MethodPost, "http://"+addr.String()+"/mcp", "good"); code != http.StatusUnauthorized {
		t.Errorf("revoked-token POST = %d, want 401", code)
	}
}

func do(t *testing.T, method, url, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestRescopeTakesEffectMidSession (audit MED-1): narrowing a connection's
// scopes stops a now-ungranted tool on the very next call of an open session.
func TestRescopeTakesEffectMidSession(t *testing.T) {
	auth := &fakeAuth{rescoped: map[uint][]string{}}
	s := NewServer(testAPI{}, auth)
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: []string{GroupNode.Scope(ModeRead), GroupWallet.Scope(ModeRead)}})
	defer done()
	if res := callTool(t, cs, "get_balances", nil); res.IsError {
		t.Fatalf("pre-rescope call failed: %+v", res.Content)
	}
	auth.rescoped[1] = []string{GroupNode.Scope(ModeRead)} // owner removes wallet
	if res := callTool(t, cs, "get_balances", nil); !res.IsError {
		t.Error("wallet tool still ran after the owner removed the wallet grant")
	}
	if res := callTool(t, cs, "get_info", nil); res.IsError {
		t.Error("still-granted tool failed after rescope")
	}
}

// TestSessionBoundToToken (audit MED-1): a valid token for connection B must not
// be able to drive a session opened by connection A.
func TestSessionBoundToToken(t *testing.T) {
	auth := &fakeAuth{byToken: map[string]*Conn{
		"tokA": {AppID: 1, Scopes: []string{GroupNode.Scope(ModeRead)}},
		"tokB": {AppID: 2, Scopes: []string{GroupNode.Scope(ModeRead)}},
	}}
	srv := httptest.NewServer(NewServer(testAPI{}, auth).Handler())
	defer srv.Close()
	post := func(tok, sid, body string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+tok)
		if sid != "" {
			req.Header.Set("Mcp-Session-Id", sid)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"a","version":"0"}}}`
	resp, _ := post("tokA", "", init)
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Skip("server did not issue a session id (stateless mode)")
	}
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	if r, _ := post("tokA", sid, list); r.StatusCode >= 300 {
		t.Fatalf("owner of the session refused: HTTP %d", r.StatusCode)
	}
	if r, body := post("tokB", sid, list); r.StatusCode < 400 {
		t.Errorf("token B drove session A: HTTP %d %s", r.StatusCode, body)
	}
}

// TestSessionCapAndServerCache (re-audit N4): a connection cannot hold more
// than maxSessionsPerConn sessions; another connection is unaffected; and the
// per-connection server is built once, not on every request.
func TestSessionCapAndServerCache(t *testing.T) {
	auth := &fakeAuth{byToken: map[string]*Conn{
		"a": {AppID: 1, Scopes: []string{GroupNode.Scope(ModeRead)}},
		"b": {AppID: 2, Scopes: []string{GroupNode.Scope(ModeRead)}},
	}}
	s := NewServer(testAPI{}, auth)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`
	open := func(tok string) int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(initBody))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := 0; i < maxSessionsPerConn; i++ {
		if c := open("a"); c != http.StatusOK {
			t.Fatalf("session %d refused below cap: %d", i, c)
		}
	}
	if c := open("a"); c != http.StatusTooManyRequests {
		t.Errorf("session over cap: status %d, want 429", c)
	}
	if c := open("b"); c != http.StatusOK {
		t.Errorf("another connection was blocked: %d", c)
	}
	s.srvMu.Lock()
	first := s.srvByApp[1].srv
	s.srvMu.Unlock()
	open("a")
	s.srvMu.Lock()
	same := s.srvByApp[1].srv == first
	s.srvMu.Unlock()
	if !same {
		t.Error("per-connection server rebuilt across requests")
	}
}

// TestLockedHubAndProxyHost covers critic O2 and O7.
func TestLockedHubAndProxyHost(t *testing.T) {
	// O2: with the hub secret unavailable, the endpoint says 503, not 401.
	locked := &StandardAuthenticator{Store: NewHMACTokenStore(func() ([]byte, error) { return nil, ErrNoSecret })}
	ls := httptest.NewServer(NewServer(testAPI{}, locked).Handler())
	defer ls.Close()
	req, _ := http.NewRequest(http.MethodPost, ls.URL+"/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer lkm_1_00")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("locked hub status = %d, want 503", resp.StatusCode)
	}

	// O7: a same-host proxy (loopback peer, public Host) reaches the hub's own
	// endpoint; the desktop loopback handler still refuses such a Host.
	auth := &fakeAuth{byToken: map[string]*Conn{"tok": {AppID: 1, Scopes: []string{GroupNode.Scope(ModeRead)}}}}
	s := NewServer(testAPI{}, auth)
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`
	status := func(h http.Handler) int {
		srv := httptest.NewServer(h)
		defer srv.Close()
		r, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(initBody))
		r.Host = "hub.example.com"
		r.Header.Set("Authorization", "Bearer tok")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		rs, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, rs.Body)
		rs.Body.Close()
		return rs.StatusCode
	}
	if c := status(s.handler(true)); c != http.StatusOK {
		t.Errorf("proxied request to the hub endpoint: %d, want 200", c)
	}
	if c := status(s.handler(false)); c == http.StatusOK {
		t.Error("desktop loopback handler accepted a non-local Host (DNS-rebinding guard off)")
	}
}
