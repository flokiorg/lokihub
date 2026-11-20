package mcp

import (
	"errors"
	"fmt"
)

// ErrInvalidArgs is returned when a tool's arguments are rejected before
// anything is queued or executed.
var ErrInvalidArgs = errors.New("mcp: invalid arguments")

func errInvalidArgs(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidArgs, msg) }

// isNodePubkey: a 33-byte compressed secp256k1 key in hex (66 chars).
func isNodePubkey(s string) bool {
	if len(s) != 66 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// isSafeAddress / isSafeID accept only plain alphanumerics (bech32/base58
// addresses, channel ids) so attacker-chosen strings cannot smuggle words,
// newlines or look-alike text into the owner's approval summary.
func isSafeAddress(s string) bool { return plainToken(s, 14, 100, false) }
func isSafeID(s string) bool      { return plainToken(s, 1, 100, true) }

func plainToken(s string, min, max int, allowSep bool) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case allowSep && (c == ':' || c == 'x' || c == '_'):
		default:
			return false
		}
	}
	return true
}
