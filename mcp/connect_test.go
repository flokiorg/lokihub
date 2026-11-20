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
