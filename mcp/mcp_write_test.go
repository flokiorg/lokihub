package mcp

import (
	"context"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func callTool(t *testing.T, cs *mcpsdk.ClientSession, name string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

// TestWriteDedupEndToEnd: the same idempotency key replays to one effect through
// the real MCP client; a new key is a new effect (M-REPLAY-1, dispatch path).
func TestWriteDedupEndToEnd(t *testing.T) {
	var calls int32
	s := NewServer(testAPI{aliasCalls: &calls}, &fakeAuth{})
	conn := &Conn{AppID: 1, Scopes: []string{GroupNode.Scope(ModeWrite)}}
	cs, done := connectInMem(t, s, conn)
	defer done()

	callTool(t, cs, "set_node_alias", map[string]any{"idempotency_key": "k1", "alias": "a"})
	callTool(t, cs, "set_node_alias", map[string]any{"idempotency_key": "k1", "alias": "a"})
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("same-key replay → %d effects, want 1", got)
	}
	callTool(t, cs, "set_node_alias", map[string]any{"idempotency_key": "k2", "alias": "b"})
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("new key → %d total effects, want 2", got)
	}
}

// TestWriteRequiresKey: a write without an idempotency key is refused and has no
// effect.
func TestWriteRequiresKey(t *testing.T) {
	var calls int32
	s := NewServer(testAPI{aliasCalls: &calls}, &fakeAuth{})
	conn := &Conn{AppID: 1, Scopes: []string{GroupNode.Scope(ModeWrite)}}
	cs, done := connectInMem(t, s, conn)
	defer done()

	res := callTool(t, cs, "set_node_alias", map[string]any{"alias": "x"})
	if res == nil || !res.IsError {
		t.Errorf("missing idempotency_key should be an error, got %+v", res)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("no-key write still executed (%d effects)", got)
	}
}

// TestWriteNeedsWriteScope: a connection with only Read on a group must not be
// able to call that group's write tools (not advertised, not executable).
func TestWriteNeedsWriteScope(t *testing.T) {
	var calls int32
	s := NewServer(testAPI{aliasCalls: &calls}, &fakeAuth{})
	conn := &Conn{AppID: 1, Scopes: []string{GroupNode.Scope(ModeRead)}}
	cs, done := connectInMem(t, s, conn)
	defer done()

	// Not in the scope-filtered catalogue.
	lt, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tl := range lt.Tools {
		if tl.Name == "set_node_alias" {
			t.Fatalf("write tool advertised to a read-only connection")
		}
	}
	// And calling it by name does not execute.
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "set_node_alias", Arguments: map[string]any{"idempotency_key": "k", "alias": "x"},
	})
	if err == nil && (res == nil || !res.IsError) {
		t.Errorf("read-only connection executed a write tool")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("write executed under read-only scope (%d effects)", got)
	}
}
