package mcp

import (
	"context"
	"sort"
	"testing"

	"github.com/flokiorg/lokihub/db"
)

// TestCatalogRatchet: every tool-classified api.API method is either wired to a
// tool adapter or explicitly deferred (with a reason) — never silently missing.
// Also: no deferred entry may name a method that is not a tool, or one that is
// already wired (stale deferral).
func TestCatalogRatchet(t *testing.T) {
	s := NewServer(testAPI{}, &fakeAuth{})
	s.SetAppResolver(func(uint) *db.App { return nil })
	s.serverForConn(&Conn{AppID: 1, Scopes: AllScopes()})

	var missing, stale []string
	for name, c := range methodClass {
		if c.Kind != ClassTool {
			continue
		}
		_, deferred := deferredTools[name]
		switch {
		case s.wired[name] && deferred:
			stale = append(stale, name)
		case !s.wired[name] && !deferred:
			missing = append(missing, name)
		}
	}
	for name, reason := range deferredTools {
		if c, ok := methodClass[name]; !ok || c.Kind != ClassTool {
			t.Errorf("deferredTools names non-tool method %q", name)
		}
		if reason == "" {
			t.Errorf("deferred %q has no reason", name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("tool methods neither wired nor deferred: %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("methods both wired and deferred (remove from deferredTools): %v", stale)
	}
	t.Logf("wired %d tool methods, deferred %d", len(s.wired), len(deferredTools))
}

// TestEveryWiredToolAdvertisedUnderFullGrant: a connection holding every scope
// sees exactly one tool per wired method, and every tool name is unique.
func TestEveryWiredToolAdvertisedUnderFullGrant(t *testing.T) {
	s := NewServer(testAPI{}, &fakeAuth{})
	s.SetAppResolver(func(uint) *db.App { return nil })
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()
	lt, err := cs.ListTools(ctxBackground(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tl := range lt.Tools {
		if seen[tl.Name] {
			t.Errorf("duplicate tool name %q", tl.Name)
		}
		seen[tl.Name] = true
		if tl.Annotations == nil {
			t.Errorf("tool %q has no annotations", tl.Name)
		}
	}
	if len(lt.Tools) != len(s.wired) {
		t.Errorf("advertised %d tools, wired %d methods", len(lt.Tools), len(s.wired))
	}
}

func ctxBackground() context.Context { return context.Background() }
