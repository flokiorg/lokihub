package mcp

import (
	"testing"

	"github.com/flokiorg/lokihub/db"
)

func appServer(apps map[uint]*db.App) *Server {
	s := NewServer(testAPI{}, &fakeAuth{})
	s.SetAppResolver(func(id uint) *db.App { return apps[id] })
	return s
}

// TestAppToolsUnknownApp: an unknown app id is refused (REST's 404), never
// reaching the core.
func TestAppToolsUnknownApp(t *testing.T) {
	s := appServer(map[uint]*db.App{})
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()
	res := callTool(t, cs, "get_app", map[string]any{"app_id": float64(999)})
	if !res.IsError {
		t.Fatalf("unknown app accepted: %+v", res)
	}
}

// TestDeleteCircleHubKindCheck: tearing down a circle via an app id that is not
// a circle hub is refused before the core is called (mirrors the REST handler).
// testAPI does not implement DeleteCircleHub, so reaching it would panic.
func TestDeleteCircleHubKindCheck(t *testing.T) {
	s := appServer(map[uint]*db.App{7: {ID: 7, Kind: db.AppKindCashHub}})
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()
	res := callTool(t, cs, "delete_circle_hub", map[string]any{
		"idempotency_key": "k", "app_id": float64(7), "mode": "refund",
	})
	if !res.IsError {
		t.Fatalf("delete_circle_hub on a cash hub was not refused: %+v", res)
	}
}

// TestAppToolsAbsentWithoutResolver: with no resolver the *db.App tools are not
// advertised at all (they could not run).
func TestAppToolsAbsentWithoutResolver(t *testing.T) {
	s := NewServer(testAPI{}, &fakeAuth{})
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()
	lt, err := cs.ListTools(ctxBackground(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range lt.Tools {
		if tl.Name == "get_app" || tl.Name == "delete_circle_hub" {
			t.Errorf("%s advertised without an app resolver", tl.Name)
		}
	}
}
