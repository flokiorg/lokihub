// Package pubkeyhex is the one definition of a Nostr public key in its hex form:
// 64 lowercase hexadecimal digits, the 32-byte x-only key. It is a leaf, so any
// package can validate a key the same way instead of repeating the length,
// case and hex checks.
//
// Valid is a format check only. It does not test that the bytes are a point on
// the curve; callers that need that parse the key themselves.
package pubkeyhex

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nbd-wtf/go-nostr/nip19"
)

// Len is the length of a hex pubkey.
const Len = 64

// Valid reports whether s is exactly 64 lowercase hex digits. Upper-case hex is
// not valid: a key is compared byte for byte (relay filters, stored rows), and
// a differently-cased copy would silently never match.
func Valid(s string) bool {
	if len(s) != Len {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Normalize trims surrounding space and lower-cases s, the form Valid accepts
// from a hand-typed key.
func Normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// FromNpub decodes a bech32 npub (NIP-19) to its hex pubkey.
func FromNpub(npub string) (string, error) {
	prefix, value, err := nip19.Decode(strings.TrimSpace(npub))
	if err != nil {
		return "", err
	}
	if prefix != "npub" {
		return "", fmt.Errorf("expected an npub, got %q", prefix)
	}
	hexpk, ok := value.(string)
	if !ok || !Valid(hexpk) {
		return "", errors.New("npub did not decode to a hex pubkey")
	}
	return hexpk, nil
}
