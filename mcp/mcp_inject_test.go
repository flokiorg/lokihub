package mcp

import (
	"strings"
	"testing"
)

// TestInstructionsStaticNoAutoWrite is the structural half of M-INJECT-1: the
// server's instructions are a compile-time constant (so attacker-controlled
// text read by a tool can never be injected into them), and they carry the
// "never write based on tool-read content" norm that blunts prompt-injection.
// The data half — read results are returned as structuredContent, never as
// instructions — is covered by TestEndToEndReadDispatch.
func TestInstructionsStaticNoAutoWrite(t *testing.T) {
	s := NewServer(testAPI{}, &fakeAuth{})
	conn := &Conn{AppID: 1, Scopes: []string{GroupNode.Scope(ModeRead)}}
	cs, done := connectInMem(t, s, conn)
	defer done()

	ir := cs.InitializeResult()
	if ir == nil || ir.Instructions == "" {
		t.Fatal("no server instructions delivered to the client")
	}
	// Instructions reaching the client are exactly the static constant.
	if ir.Instructions != instructions {
		t.Error("delivered instructions diverged from the static constant (runtime injection risk)")
	}
	// The no-auto-write norm is present.
	for _, want := range []string{"Never initiate", "tool result"} {
		if !strings.Contains(ir.Instructions, want) {
			t.Errorf("instructions missing the no-auto-write norm fragment %q", want)
		}
	}
}
