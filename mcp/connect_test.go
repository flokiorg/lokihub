package mcp

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestScopesFor(t *testing.T) {
	got, err := ScopesFor(map[Group]Mode{GroupWallet: ModeWrite, GroupNode: ModeRead})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mcp:node:read", "mcp:wallet:read", "mcp:wallet:write"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScopesFor = %v, want %v", got, want)
	}
	if _, err := ScopesFor(nil); !errors.Is(err, ErrNoGrants) {
		t.Errorf("empty grants err = %v, want ErrNoGrants", err)
	}
	if _, err := ScopesFor(map[Group]Mode{"bogus": ModeRead}); err == nil {
		t.Error("unknown group accepted")
	}
	if _, err := ScopesFor(map[Group]Mode{GroupWallet: "admin"}); err == nil {
		t.Error("invalid mode accepted")
	}
}

// TestCreateConnectionRoundTrip: a minted token resolves (via the real
// StandardAuthenticator) to a connection carrying exactly the granted scopes.
func TestCreateConnectionRoundTrip(t *testing.T) {
	loader := &fakeLoader{apps: map[uint]AppInfo{}, gone: map[uint]bool{}}
	var next uint = 10
	create := func(name string, exp *time.Time, scopes []string) (uint, error) {
		next++
		loader.apps[next] = AppInfo{Name: name, Kind: AppKindMCP, Scopes: scopes, ExpiresAt: exp}
		return next, nil
	}
	tokens := NewHMACTokenStore(constSecret("s"))

	id, tok, err := CreateConnection(create, tokens, "assistant", map[Group]Mode{GroupChannels: ModeRead}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := &StandardAuthenticator{Store: tokens, Apps: loader}
	conn, err := a.Resolve(context.Background(), tok)
	if err != nil {
		t.Fatalf("Resolve minted token: %v", err)
	}
	if conn.AppID != id || !reflect.DeepEqual(conn.Scopes, []string{"mcp:channels:read"}) {
		t.Errorf("conn = %+v", conn)
	}
	// Deleting the App revokes the token.
	loader.gone[id] = true
	if _, err := a.Resolve(context.Background(), tok); !errors.Is(err, ErrRevoked) {
		t.Errorf("after delete, Resolve err = %v, want ErrRevoked", err)
	}
	if _, _, err := CreateConnection(create, tokens, "", map[Group]Mode{GroupNode: ModeRead}, nil); err == nil {
		t.Error("empty name accepted")
	}
}

func TestGrantsFromScopesRoundTrip(t *testing.T) {
	in := map[Group]Mode{GroupWallet: ModeWrite, GroupNode: ModeRead, GroupListings: ModeWrite}
	scopes, err := ScopesFor(in)
	if err != nil {
		t.Fatal(err)
	}
	got := GrantsFromScopes(append(scopes, "pay_invoice", "mcp:bogus:read"))
	if !reflect.DeepEqual(got, in) {
		t.Errorf("GrantsFromScopes = %v, want %v", got, in)
	}
}

// TestCleanConnectionName (pentest E MED-2): lookalike names collapse to the
// same string, so the core's duplicate check sees them.
func TestCleanConnectionName(t *testing.T) {
	for in, want := range map[string]string{
		"Desk assistant\u200b":          "Desk assistant",
		"Desk\u00a0assistant":           "Desk assistant",
		"  Desk   assistant  ":          "Desk assistant",
		"Ｄｅｓｋ assistant":                "Desk assistant",
		"PENTEST \u202egnp.exe\u202c x": "PENTEST gnp.exe x",
		"a\nb\tc":                       "a b c",
		"\u200b\u200b":                  "",
	} {
		if got := CleanConnectionName(in); got != want {
			t.Errorf("CleanConnectionName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNameSkeleton (pentest R2-3 MED-4): visually identical names collide.
func TestNameSkeleton(t *testing.T) {
	base := NameSkeleton("Desk assistant")
	for _, n := range []string{
		"Desk assistant\u3164", "Desk assistant\u1160", "Desk\u2800assistant", "Desk\u034f assistant",
		"Desk assistant\ufe0f", "Desk assistant\u17b5", "Desk \u0430ssistant", "D\u0435sk assistant",
		"desk assistant", "DESK ASSISTANT",
	} {
		if NameSkeleton(n) != base {
			t.Errorf("lookalike %q has a different skeleton (%q vs %q)", n, NameSkeleton(n), base)
		}
	}
	if NameSkeleton("Kitchen helper") == base {
		t.Error("different names collide")
	}
}
