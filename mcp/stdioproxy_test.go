package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStdioProxyRoundTrip drives the stdio bridge against the real /mcp handler:
// initialize → initialized → tools/list → tools/call, one JSON-RPC message per
// line in each direction, session id carried across requests.
func TestStdioProxyRoundTrip(t *testing.T) {
	auth := &fakeAuth{byToken: map[string]*Conn{
		"tok": {AppID: 1, Scopes: []string{GroupNode.Scope(ModeRead), GroupWallet.Scope(ModeRead)}},
	}}
	ts := httptest.NewServer(NewServer(testAPI{}, auth).Handler())
	defer ts.Close()

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"stdio-test","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_info","arguments":{}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	p := &StdioProxy{Endpoint: ts.URL + "/mcp", Token: "tok"}
	if err := p.Run(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatalf("proxy run: %v\nout so far:\n%s", err, out.String())
	}

	byID := map[float64]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("non-JSON line on stdout: %q", line)
		}
		if id, ok := m["id"].(float64); ok {
			byID[id] = m
		}
	}
	for _, id := range []float64{1, 2, 3} {
		if byID[id] == nil {
			t.Fatalf("no response for id %v; out:\n%s", id, out.String())
		}
		if byID[id]["error"] != nil {
			t.Fatalf("id %v error: %v", id, byID[id]["error"])
		}
	}
	res2, _ := json.Marshal(byID[2]["result"])
	if !strings.Contains(string(res2), `"get_info"`) || strings.Contains(string(res2), `"list_channels"`) {
		t.Errorf("tools/list through proxy not scope-filtered: %s", res2)
	}
	res3, _ := json.Marshal(byID[3]["result"])
	if strings.Contains(string(res3), `"isError":true`) {
		t.Errorf("get_info through proxy errored: %s", res3)
	}
	if p.sessionID == "" {
		t.Error("session id not captured/carried")
	}
}

// TestStdioProxyRejectedToken: a bad/revoked token surfaces as a clear error,
// not a hang or silent success.
func TestStdioProxyRejectedToken(t *testing.T) {
	ts := httptest.NewServer(NewServer(testAPI{}, &fakeAuth{byToken: map[string]*Conn{}}).Handler())
	defer ts.Close()
	p := &StdioProxy{Endpoint: ts.URL + "/mcp", Token: "revoked"}
	err := p.Run(context.Background(),
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n"), &bytes.Buffer{})
	if !errors.Is(err, ErrProxyUnauthorized) {
		t.Errorf("err = %v, want ErrProxyUnauthorized", err)
	}
}

// TestStdioProxySurvivesSessionExpiry (re-audit N6): when the hub forgets the
// session (idle timeout), the bridge re-initializes and the next call succeeds
// instead of the bridge exiting.
func TestStdioProxySurvivesSessionExpiry(t *testing.T) {
	auth := &fakeAuth{byToken: map[string]*Conn{"tok": {AppID: 1, Scopes: []string{GroupNode.Scope(ModeRead)}}}}
	ts := httptest.NewServer(NewServer(testAPI{}, auth).Handler())
	defer ts.Close()
	p := &StdioProxy{Endpoint: ts.URL + "/mcp", Token: "tok"}
	var out bytes.Buffer
	first := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	if err := p.Run(context.Background(), strings.NewReader(first), &out); err != nil {
		t.Fatal(err)
	}
	p.sessionID = "expired-session-id" // simulate the hub having reaped it
	out.Reset()
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_info","arguments":{}}}` + "\n"
	if err := p.Run(context.Background(), strings.NewReader(call), &out); err != nil {
		t.Fatalf("bridge died on an expired session: %v", err)
	}
	if !strings.Contains(out.String(), `"id":2`) || strings.Contains(out.String(), `"isError":true`) {
		t.Errorf("call after re-init failed: %s", out.String())
	}
}
