package mcp

import (
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
	for i := 0; i < activityCap+25; i++ {
		l.Add(ActivityEntry{AppID: 1, Tool: "t"})
	}
	if n := len(l.List(0, activityCap)); n != activityCap {
		t.Errorf("ring holds %d, want cap %d", n, activityCap)
	}
	if c := clip("a\nFAKE LOG LINE", 100); strings.ContainsAny(c, "\n\r") {
		t.Errorf("control characters survive clip: %q", c)
	}
}
