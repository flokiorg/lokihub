package mcp

import "errors"

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
}

// noControls is the default: never paused. Every irreversible action always
// goes through owner approval (there is no pre-approved address list — owner
// decision D20).
type noControls struct{}

func (noControls) Paused() bool { return false }

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
