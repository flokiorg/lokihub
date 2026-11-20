package mcp

import (
	"strings"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeControls struct {
	paused atomic.Bool
	allow  map[string]bool
}

func (f *fakeControls) Paused() bool                { return f.paused.Load() }
func (f *fakeControls) RedeemAllowed(a string) bool { return f.allow[a] }

// TestPauseBlocksLiveSession: pausing takes effect on the next call of an
// already-open session (reads, writes and guarded alike), and unpausing restores
// service — the switch is checked per call, not at connect.
func TestPauseBlocksLiveSession(t *testing.T) {
	var aliases int32
	ctl := &fakeControls{}
	s := NewServer(testAPI{aliasCalls: &aliases}, &fakeAuth{})
	s.SetControls(ctl)
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()

	if res := callTool(t, cs, "get_info", nil); res.IsError {
		t.Fatalf("pre-pause read failed: %+v", res.Content)
	}
	ctl.paused.Store(true)
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"get_info", nil},
		{"set_node_alias", map[string]any{"idempotency_key": "p1", "alias": "x"}},
		{"redeem_onchain_funds", map[string]any{"idempotency_key": "p2", "to_address": "bc1q", "amount": float64(1)}},
	} {
		res := callTool(t, cs, c.name, c.args)
		if !res.IsError {
			t.Errorf("%s ran while paused", c.name)
		} else if txt := resultText(res); !strings.Contains(txt, "paused") {
			t.Errorf("%s failed for the wrong reason while paused: %q", c.name, txt)
		}
	}
	if atomic.LoadInt32(&aliases) != 0 {
		t.Error("write executed while paused")
	}
	if n := len(s.Pending().List(1)); n != 0 {
		t.Errorf("guarded action queued while paused (%d)", n)
	}
	ctl.paused.Store(false)
	if res := callTool(t, cs, "get_info", nil); res.IsError {
		t.Error("read still failing after unpause")
	}
}

// TestRedeemAllowlist: a redeem to an owner allow-listed address executes
// immediately (once, deduped); any other address is queued for approval; a
// case-variant of an allow-listed bech32 address is matched (normalized), while
// a lookalike is not.
func TestRedeemAllowlist(t *testing.T) {
	var redeems int32
	ctl := &fakeControls{allow: map[string]bool{"bc1qowncoldwallet": true}}
	s := NewServer(testAPI{redeemCalls: &redeems}, &fakeAuth{})
	s.SetControls(ctl)
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: []string{GroupWallet.Scope(ModeWrite)}})
	defer done()

	args := map[string]any{"idempotency_key": "a1", "to_address": "BC1QownColdWallet", "amount": float64(5)}
	callTool(t, cs, "redeem_onchain_funds", args)
	callTool(t, cs, "redeem_onchain_funds", args) // replay
	if got := atomic.LoadInt32(&redeems); got != 1 {
		t.Fatalf("allow-listed redeem executions = %d, want exactly 1", got)
	}
	if n := openPending(s, 1); n != 0 {
		t.Fatalf("allow-listed redeem was queued for approval (%d open)", n)
	}

	// Lookalike (extra char) is NOT allow-listed → queued, not executed.
	callTool(t, cs, "redeem_onchain_funds", map[string]any{"idempotency_key": "a2", "to_address": "bc1qowncoldwallet1", "amount": float64(5)})
	if got := atomic.LoadInt32(&redeems); got != 1 {
		t.Fatalf("lookalike address executed (%d)", got)
	}
	if n := openPending(s, 1); n != 1 {
		t.Fatalf("lookalike redeem not queued (%d open)", n)
	}
}

func resultText(r *mcpsdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func openPending(s *Server, appID uint) int {
	n := 0
	for _, a := range s.Pending().List(appID) {
		if a.Status == PendingOpen {
			n++
		}
	}
	return n
}
