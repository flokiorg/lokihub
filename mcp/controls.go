package mcp

import (
	"errors"
	"strings"
)

// ErrPaused is returned for every tool call while the owner has paused all AI
// assistants. It is checked per call, so a pause takes effect on live sessions
// immediately (no TOCTOU window beyond a call already executing).
var ErrPaused = errors.New("mcp: AI assistants are paused by the hub owner")

// Controls are the owner's global levers over every MCP connection. The hub
// backs them with its persistent config so a pause survives a restart (an
// in-memory pause that reset to "running" would fail open).
type Controls interface {
	// Paused reports the global "Pause all AI assistants" switch. An error
	// reading it must be treated as paused (fail closed).
	Paused() bool
	// RedeemAllowed reports whether an on-chain address is on the owner's
	// pre-approved withdrawal allow-list; a redeem to such an address runs
	// without a per-action approval.
	RedeemAllowed(address string) bool
}

// noControls is the default: never paused, empty allow-list (every irreversible
// action goes through owner approval).
type noControls struct{}

func (noControls) Paused() bool              { return false }
func (noControls) RedeemAllowed(string) bool { return false }

// SetControls installs the owner's global controls.
func (s *Server) SetControls(c Controls) {
	if c == nil {
		c = noControls{}
	}
	s.controls = c
}

func (s *Server) checkPaused() error {
	if s.controls.Paused() {
		return ErrPaused
	}
	return nil
}

// NormalizeAddress canonicalizes an on-chain address for allow-list matching
// (trimmed, lower-cased: bech32 is case-insensitive, and a mixed-case variant of
// an allow-listed address must not slip past or be refused).
func NormalizeAddress(a string) string { return strings.ToLower(strings.TrimSpace(a)) }
