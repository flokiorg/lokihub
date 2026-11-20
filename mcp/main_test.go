package mcp

import (
	"os"
	"testing"

	"golang.org/x/time/rate"
)

// TestMain lifts the per-connection call rate for unit tests that make rapid
// calls; TestCallRateLimit restores it.
func TestMain(m *testing.M) {
	callRateLimit = rate.Inf
	os.Exit(m.Run())
}

// TestCallRateLimit (pentest F F-2): a connection calling far faster than any
// real assistant is throttled; another connection is unaffected.
func TestCallRateLimit(t *testing.T) {
	prev := callRateLimit
	callRateLimit = rate.Limit(callRatePerSec)
	defer func() { callRateLimit = prev }()
	s := NewServer(testAPI{}, &fakeAuth{})
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()
	limited := false
	for i := 0; i < callBurst+20; i++ {
		if r := callTool(t, cs, "get_info", map[string]any{}); r.IsError {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("flood was never throttled")
	}
	cs2, done2 := connectInMem(t, s, &Conn{AppID: 2, Scopes: AllScopes()})
	defer done2()
	if r := callTool(t, cs2, "get_info", map[string]any{}); r.IsError {
		t.Error("another connection was throttled")
	}
}
