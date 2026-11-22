package mcp

import (
	"context"
	"testing"
)

func constSecret(v string) func() ([]byte, error) {
	return func() ([]byte, error) { return []byte(v), nil }
}

func TestHMACTokenRoundTrip(t *testing.T) {
	s := NewHMACTokenStore(constSecret("hub-secret-xyz"))
	ctx := context.Background()

	tok, err := s.Mint(42)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if id, ok := s.AppID(ctx, tok); !ok || id != 42 {
		t.Fatalf("AppID(mint(42)) = (%d,%v), want (42,true)", id, ok)
	}
}

func TestHMACTokenRejects(t *testing.T) {
	s := NewHMACTokenStore(constSecret("hub-secret-xyz"))
	ctx := context.Background()
	good, err := s.Mint(7)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	bad := []string{
		"",
		"garbage",
		"lkm_7_deadbeef",                // wrong mac
		"lkm_8_" + good[len("lkm_7_"):], // right mac, mismatched id
		good[:len(good)-1],              // truncated mac
		good + "x",                      // extended mac
		"lkm_7",                         // no mac
		"lkm__abc",                      // empty id
		"Bearer " + good,                // scheme should be stripped before AppID
	}
	for _, b := range bad {
		if id, ok := s.AppID(ctx, b); ok {
			t.Errorf("AppID(%q) = (%d,true), want rejected", b, id)
		}
	}

	// A token from a different secret must not validate.
	other := NewHMACTokenStore(constSecret("different-secret"))
	otherTok, _ := other.Mint(7)
	if _, ok := s.AppID(ctx, otherTok); ok {
		t.Error("token from a different secret validated")
	}

	// With no secret (locked hub), nothing validates and minting errors.
	locked := NewHMACTokenStore(func() ([]byte, error) { return nil, ErrNoSecret })
	if _, err := locked.Mint(7); err == nil {
		t.Error("Mint succeeded with no secret")
	}
	if _, ok := locked.AppID(ctx, good); ok {
		t.Error("locked store validated a token")
	}
}
