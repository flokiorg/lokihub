package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

// TestResidualGuard is the core of the irreversible-class safety (ARCHITECTURE
// §7 / audit S-CRIT-2): a guarded tool call must NOT execute on its own — it
// queues a pending action for owner approval, a retry does not stack duplicates,
// and only approval executes it, exactly once.
func TestResidualGuard(t *testing.T) {
	var redeems int32
	s := NewServer(testAPI{redeemCalls: &redeems}, &fakeAuth{})
	conn := &Conn{AppID: 1, Scopes: []string{GroupWallet.Scope(ModeWrite)}}
	cs, done := connectInMem(t, s, conn)
	defer done()

	args := map[string]any{
		"idempotency_key": "r1",
		"to_address":      "bc1qattackercontrolledaddress",
		"amount":          float64(100000),
		"send_all":        false,
	}

	// Guarded call → queued, NOT executed.
	res := callTool(t, cs, "redeem_onchain_funds", args)
	if res.IsError {
		t.Fatalf("guarded call errored: %+v", res.Content)
	}
	if got := atomic.LoadInt32(&redeems); got != 0 {
		t.Fatalf("guarded op executed WITHOUT approval (%d) — unrecoverable drain path open", got)
	}
	acts := s.Pending().List(1)
	if len(acts) != 1 || acts[0].Status != PendingOpen {
		t.Fatalf("expected one open pending action, got %+v", acts)
	}
	id := acts[0].ID

	// Retry with the same key → still one pending action, still not executed.
	callTool(t, cs, "redeem_onchain_funds", args)
	if n := len(s.Pending().List(1)); n != 1 {
		t.Fatalf("retry stacked %d pending actions, want 1", n)
	}
	if got := atomic.LoadInt32(&redeems); got != 0 {
		t.Fatalf("executed on retry (%d)", got)
	}

	// Owner approves → executes exactly once.
	if _, err := s.Pending().Approve(id); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got := atomic.LoadInt32(&redeems); got != 1 {
		t.Fatalf("after approval, executions = %d, want 1", got)
	}
	// Double approval does not re-execute (no double-spend on re-approve).
	if _, err := s.Pending().Approve(id); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	if got := atomic.LoadInt32(&redeems); got != 1 {
		t.Fatalf("double approval re-executed (%d)", got)
	}
}

// TestResidualGuardDeny: a denied action never executes and cannot later be
// approved (confirm-bypass attempt).
func TestResidualGuardDeny(t *testing.T) {
	var redeems int32
	s := NewServer(testAPI{redeemCalls: &redeems}, &fakeAuth{})
	conn := &Conn{AppID: 1, Scopes: []string{GroupWallet.Scope(ModeWrite)}}
	cs, done := connectInMem(t, s, conn)
	defer done()

	callTool(t, cs, "redeem_onchain_funds", map[string]any{
		"idempotency_key": "r2", "to_address": "bc1qxyzxyzxyzxyzxyz", "amount": float64(1), "send_all": false,
	})
	id := s.Pending().List(1)[0].ID

	if err := s.Pending().Deny(id); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if _, err := s.Pending().Approve(id); err != ErrPendingDenied {
		t.Errorf("approve after deny = %v, want ErrPendingDenied", err)
	}
	if got := atomic.LoadInt32(&redeems); got != 0 {
		t.Errorf("denied action executed (%d)", got)
	}
}

// TestGuardedNeedsScope: a read-only wallet grant must not advertise or reach
// the guarded on-chain redeem.
func TestGuardedNeedsScope(t *testing.T) {
	var redeems int32
	s := NewServer(testAPI{redeemCalls: &redeems}, &fakeAuth{})
	conn := &Conn{AppID: 1, Scopes: []string{GroupWallet.Scope(ModeRead)}}
	cs, done := connectInMem(t, s, conn)
	defer done()

	lt, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tl := range lt.Tools {
		if tl.Name == "redeem_onchain_funds" {
			t.Fatal("guarded redeem advertised to a read-only connection")
		}
	}
	if len(s.Pending().List(1)) != 0 {
		t.Error("pending action created without write scope")
	}
}

// TestApprovalSummaryIsComplete (audit HIGH-1): the owner's approval text must
// describe every parameter that will execute — a sweep must say so, a force
// close must say so — and contradictory or spoof-capable args never queue.
func TestApprovalSummaryIsComplete(t *testing.T) {
	s := NewServer(testAPI{}, &fakeAuth{})
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()
	pk := strings.Repeat("02", 1) + strings.Repeat("ab", 32)

	callTool(t, cs, "redeem_onchain_funds", map[string]any{"idempotency_key": "s1", "to_address": "bc1qsweeptargetaddr", "send_all": true})
	callTool(t, cs, "close_channel", map[string]any{"idempotency_key": "c1", "peer_id": pk, "channel_id": "123x1x0", "force": true})
	sums := map[string]string{}
	for _, a := range s.Pending().ListAll() {
		sums[a.Tool] = a.Summary
	}
	if !strings.Contains(sums["redeem_onchain_funds"], "ALL") {
		t.Errorf("sweep summary hides send_all: %q", sums["redeem_onchain_funds"])
	}
	if !strings.Contains(sums["close_channel"], "FORCE") {
		t.Errorf("force-close summary hides force: %q", sums["close_channel"])
	}

	before := len(s.Pending().ListAll())
	for _, bad := range []struct {
		tool string
		args map[string]any
	}{
		{"redeem_onchain_funds", map[string]any{"idempotency_key": "b1", "to_address": "bc1qsweeptargetaddr", "amount": float64(5), "send_all": true}},
		{"redeem_onchain_funds", map[string]any{"idempotency_key": "b2", "to_address": "bc1qsweeptargetaddr"}},
		{"redeem_onchain_funds", map[string]any{"idempotency_key": "b3", "to_address": "bc1q legit\nAPPROVED BY OWNER", "amount": float64(5)}},
		{"open_channel", map[string]any{"idempotency_key": "b4", "pubkey": pk, "amount_loki": float64(0)}},
		{"open_channel", map[string]any{"idempotency_key": "b5", "pubkey": pk, "amount_loki": float64(-5)}},
		{"open_channel", map[string]any{"idempotency_key": "b6", "pubkey": "not-a-key", "amount_loki": float64(5)}},
		{"close_channel", map[string]any{"idempotency_key": "b7", "peer_id": "x", "channel_id": "1"}},
	} {
		if res := callTool(t, cs, bad.tool, bad.args); !res.IsError {
			t.Errorf("%s accepted invalid args %v", bad.tool, bad.args)
		}
	}
	if after := len(s.Pending().ListAll()); after != before {
		t.Errorf("invalid calls queued %d actions", after-before)
	}
}

// TestPendingBoundsAndOwnerChecks (audit MED-2 / LOW-2): a connection cannot
// flood the owner's queue; the owner cannot approve while paused or for a
// revoked connection.
func TestPendingBoundsAndOwnerChecks(t *testing.T) {
	auth := &fakeAuth{dead: map[uint]bool{}}
	ctl := &fakeControls{}
	var redeems int32
	s := NewServer(testAPI{redeemCalls: &redeems}, auth)
	s.SetControls(ctl)
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: []string{GroupWallet.Scope(ModeWrite)}})
	defer done()
	call := func(key string) bool {
		res := callTool(t, cs, "redeem_onchain_funds", map[string]any{"idempotency_key": key, "to_address": "bc1qfloodtargetaddrx", "amount": float64(1)})
		return !res.IsError
	}
	for i := 0; i < maxOpenPendingPerApp; i++ {
		if !call(fmt.Sprintf("f%d", i)) {
			t.Fatalf("request %d refused below the cap", i)
		}
	}
	if call("over-cap") {
		t.Errorf("connection exceeded %d open requests", maxOpenPendingPerApp)
	}
	id := s.Pending().List(1)[0].ID

	ctl.paused.Store(true)
	if _, err := s.ApprovePending(context.Background(), id); !errors.Is(err, ErrPaused) {
		t.Errorf("approve while paused err = %v", err)
	}
	ctl.paused.Store(false)
	auth.dead[1] = true
	if _, err := s.ApprovePending(context.Background(), id); !errors.Is(err, ErrRevoked) {
		t.Errorf("approve for revoked connection err = %v", err)
	}
	if atomic.LoadInt32(&redeems) != 0 {
		t.Error("an action executed")
	}
}

// TestAllowlistAndQueueShareOneRecord (re-audit N1): a key first queued, then
// retried after the owner allow-lists the address, must NOT execute directly —
// and approving the queued record executes exactly once in total.
func TestAllowlistAndQueueShareOneRecord(t *testing.T) {
	var redeems int32
	ctl := &fakeControls{allow: map[string]bool{}}
	s := NewServer(testAPI{redeemCalls: &redeems}, &fakeAuth{})
	s.SetControls(ctl)
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: []string{GroupWallet.Scope(ModeWrite)}})
	defer done()
	args := map[string]any{"idempotency_key": "K", "to_address": "bc1qsametargetaddrx", "amount": float64(100)}

	callTool(t, cs, "redeem_onchain_funds", args) // queued
	ctl.allow["bc1qsametargetaddrx"] = true       // owner allow-lists it later
	res := callTool(t, cs, "redeem_onchain_funds", args)
	if atomic.LoadInt32(&redeems) != 0 {
		t.Fatalf("retry after allow-listing executed directly (%d) — would double-send on approval", redeems)
	}
	if !strings.Contains(resultText(res)+fmt.Sprint(res.StructuredContent), "pending_approval") {
		t.Errorf("retry did not report the existing queued record: %+v", res.StructuredContent)
	}
	id := s.Pending().List(1)[0].ID
	if _, err := s.Pending().Approve(id); err != nil {
		t.Fatal(err)
	}
	callTool(t, cs, "redeem_onchain_funds", args) // another retry after approval
	if got := atomic.LoadInt32(&redeems); got != 1 {
		t.Fatalf("total executions = %d, want exactly 1", got)
	}
	// Reusing the key with different args is a conflict, not a stale answer.
	bad := map[string]any{"idempotency_key": "K", "to_address": "bc1qsametargetaddrx", "amount": float64(999)}
	if r := callTool(t, cs, "redeem_onchain_funds", bad); !r.IsError {
		t.Error("key reused with different amount was accepted")
	}
}
