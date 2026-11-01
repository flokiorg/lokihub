package lspservice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	nostr "github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip46"
)

// TestSignNIP98_VerifiesUnderGoNostr is the load-bearing cross-library check:
// lspd verifies the Authorization event with nbd-wtf/go-nostr's CheckSignature,
// while the Hub signs it with nmilat. This asserts an nmilat-signed NIP-98 event
// round-trips through go-nostr (plan §3.3 "lspd's go-nostr CheckSignature accepts
// it regardless of which nostr library produced it") and carries the exact tags
// lspd's verifier requires: u, m (not "method"), and payload.
func TestSignNIP98_VerifiesUnderGoNostr(t *testing.T) {
	// A deterministic test key (32-byte hex). NewLocalKey accepts hex or nsec.
	key, err := nip46.NewLocalKey("0000000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		t.Fatalf("NewLocalKey: %v", err)
	}

	c := &Client{}
	const url = "http://lspd:8080/dashboard"
	body := []byte(`{"jsonrpc":"2.0","method":"get_summary","id":1}`)

	header, err := c.signNIP98(context.Background(), key, url, "POST", body)
	if err != nil {
		t.Fatalf("signNIP98: %v", err)
	}
	if !strings.HasPrefix(header, "Nostr ") {
		t.Fatalf("header missing Nostr prefix: %q", header)
	}

	evJSON, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Nostr "))
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}

	// Unmarshal into go-nostr's Event — the exact type lspd uses — and verify.
	var ev nostr.Event
	if err := json.Unmarshal(evJSON, &ev); err != nil {
		t.Fatalf("unmarshal into go-nostr Event: %v", err)
	}
	if ev.Kind != 27235 {
		t.Fatalf("kind = %d, want 27235", ev.Kind)
	}
	if ev.PubKey != key.PubKey() {
		t.Fatalf("pubkey = %q, want %q", ev.PubKey, key.PubKey())
	}
	ok, err := ev.CheckSignature()
	if err != nil || !ok {
		t.Fatalf("go-nostr CheckSignature ok=%v err=%v (nmilat signature rejected)", ok, err)
	}

	// lspd matches on "u", "m" and "payload" tags (lspd/transport/auth/nostr.go).
	tags := map[string]string{}
	for _, tag := range ev.Tags {
		if len(tag) >= 2 {
			tags[tag[0]] = tag[1]
		}
	}
	if tags["u"] != url {
		t.Errorf("u tag = %q, want %q", tags["u"], url)
	}
	if tags["m"] != "POST" {
		t.Errorf("m tag = %q, want POST (lspd requires 'm', not 'method')", tags["m"])
	}
	if tags["payload"] == "" {
		t.Error("missing payload tag")
	}
	if _, hasMethod := tags["method"]; hasMethod {
		t.Error("unexpected 'method' tag: lspd matches 'm', emitting 'method' would 401")
	}
}

// TestResolveSigner_NsecDispatch checks the raw-key branch of the signer
// dispatch yields a usable local signer.
func TestResolveSigner_NsecDispatch(t *testing.T) {
	s, closeFn, err := resolveSigner(context.Background(), "0000000000000000000000000000000000000000000000000000000000000001", "")
	if err != nil {
		t.Fatalf("resolveSigner: %v", err)
	}
	defer closeFn()
	if len(s.PubKey()) != 64 {
		t.Fatalf("pubkey %q is not 64-hex", s.PubKey())
	}
}

// TestResolveSigner_RequiresExactlyOne rejects both-or-neither credentials.
func TestResolveSigner_RequiresExactlyOne(t *testing.T) {
	if _, _, err := resolveSigner(context.Background(), "", ""); err == nil {
		t.Error("expected error when neither nsec nor bunker is set")
	}
	if _, _, err := resolveSigner(context.Background(), "abc", "bunker://x"); err == nil {
		t.Error("expected error when both nsec and bunker are set")
	}
}
