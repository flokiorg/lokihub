package mcp

import (
	"testing"

	"github.com/flokiorg/lokihub/db"
)

// TestAssistantDeleteGuards (audit MED-5).
func TestAssistantDeleteGuards(t *testing.T) {
	apps := map[uint]*db.App{
		1: {ID: 1, Kind: AppKindMCP},        // this assistant
		2: {ID: 2, Kind: AppKindMCP},        // another assistant
		3: {ID: 3, Kind: db.AppKindCashHub}, // a cash hub
	}
	s := appServer(apps)
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()
	if res := callTool(t, cs, "delete_app", map[string]any{"idempotency_key": "d1", "app_id": float64(2)}); !res.IsError {
		t.Error("assistant revoked another assistant's connection")
	}
	if res := callTool(t, cs, "delete_app_confirmed", map[string]any{"idempotency_key": "d2", "app_id": float64(3), "confirm_watch_end": true}); !res.IsError {
		t.Error("assistant self-asserted confirm_watch_end")
	}
}
