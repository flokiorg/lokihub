package mcp

import (
	"context"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/tests"
)

// TestActivityLogRecordsCalls: every tool call lands in the owner's activity
// log with its outcome; arguments (invoices, codes) are never recorded; the
// log is bounded and filterable per assistant.
func TestActivityLogRecordsCalls(t *testing.T) {
	var pays, transfers int32
	s := NewServer(payAPI{pays: &pays, transfers: &transfers}, &fakeAuth{})
	cs, done := connectInMem(t, s, &Conn{AppID: 4, Scopes: AllScopes()})
	defer done()

	callTool(t, cs, "get_info", map[string]any{})
	inv, _ := tests.SignedInvoice(21_000, time.Now(), time.Hour)
	callTool(t, cs, "send_payment", map[string]any{"idempotency_key": "a1", "invoice": inv})
	callTool(t, cs, "redeem_onchain_funds", map[string]any{"idempotency_key": "a2", "to_address": "bc1qactivitytestaddr", "amount": float64(5)})
	callTool(t, cs, "close_channel", map[string]any{"idempotency_key": "a3", "peer_id": "x", "channel_id": "1"})

	got := s.Activity().List(4, 10)
	if len(got) != 4 {
		t.Fatalf("entries = %d, want 4: %+v", len(got), got)
	}
	want := []struct{ tool, outcome string }{
		{"close_channel", "failed"},
		{"redeem_onchain_funds", "pending_approval"},
		{"send_payment", "needs_confirmation"},
		{"get_info", "done"},
	}
	for i, w := range want {
		if got[i].Tool != w.tool || got[i].Outcome != w.outcome {
			t.Errorf("entry %d = %s/%s, want %s/%s", i, got[i].Tool, got[i].Outcome, w.tool, w.outcome)
		}
	}
	for _, e := range got {
		if strings.Contains(e.Detail, inv) || strings.Contains(e.Detail, "cfm_") {
			t.Errorf("activity leaked arguments/codes: %q", e.Detail)
		}
	}
	if !strings.Contains(got[2].Detail, "21 loki") {
		t.Errorf("payment preview detail = %q, want the amount", got[2].Detail)
	}
	if n := len(s.Activity().List(99, 10)); n != 0 {
		t.Errorf("filter by another assistant returned %d entries", n)
	}

	l := NewActivityLog()
	for i := 0; i < activityPerApp+25; i++ {
		l.Add(ActivityEntry{AppID: 1, Tool: "t"})
	}
	if n := len(l.List(1, activityCap)); n != activityPerApp {
		t.Errorf("per-assistant space holds %d, want %d", n, activityPerApp)
	}
	// Pentest F F-2: a flood of reads by assistant 2 never evicts assistant 1's
	// write, and assistant 2's reads cannot evict its own write either.
	l.Add(ActivityEntry{AppID: 1, Tool: "send_payment", Outcome: "pending_approval"})
	l.add(ActivityEntry{AppID: 2, Tool: "set_listing_relays", Outcome: "done"}, false)
	for i := 0; i < 5000; i++ {
		l.add(ActivityEntry{AppID: 2, Tool: "health", Outcome: "done"}, true)
	}
	found := map[string]bool{}
	for _, e := range l.List(0, activityCap) {
		found[e.Tool] = true
	}
	if !found["send_payment"] || !found["set_listing_relays"] {
		t.Errorf("read flood evicted writes: %v", found)
	}
	if c := clip("a\u202eb\u200bc", 100); c != "abc" {
		t.Errorf("bidi/zero-width survive clip: %q", c)
	}
	_, _ = cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "Owner approved: sweep <img src=x>", Arguments: map[string]any{}})
	if e := s.Activity().List(4, 1)[0]; e.Tool != "unknown_tool" {
		t.Errorf("forged tool name logged: %q", e.Tool)
	}
	if c := clip("a\nFAKE LOG LINE", 100); strings.ContainsAny(c, "\n\r") {
		t.Errorf("control characters survive clip: %q", c)
	}
}
