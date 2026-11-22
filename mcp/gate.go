package mcp

import (
	"errors"
	"fmt"
)

// ErrNotATool is returned when an api.API method is reached that is not exposed
// as a tool (it is Excluded or NotApplicable). This is the structural backstop:
// even if a caller names an unexposed operation, the gate refuses it.
var ErrNotATool = errors.New("mcp: operation is not exposed as a tool")

// ScopeError reports that a connection lacked the scope a tool requires.
type ScopeError struct {
	Needed string
}

func (e *ScopeError) Error() string {
	return fmt.Sprintf("mcp: missing scope %q", e.Needed)
}

// Gate is the single, load-bearing authorization check. It is a pure function
// over the connection's granted scopes and a method's classification, so it is
// exhaustively unit-testable with no node, DB, or network.
//
// A tool runs only if the connection holds the exact scope its group+mode
// requires. Excluded / NotApplicable methods never run, regardless of scopes —
// this is why a mis-named or future-added core method cannot be reached without
// an explicit, reviewed classification entry (see policy.go + the ratchet test).
func Gate(scopes []string, c Classification) error {
	if c.Kind != ClassTool {
		return ErrNotATool
	}
	needed := c.Group.Scope(c.Mode)
	for _, s := range scopes {
		if s == needed {
			return nil
		}
	}
	return &ScopeError{Needed: needed}
}

// Allowed is a boolean convenience wrapper around Gate.
func Allowed(scopes []string, c Classification) bool {
	return Gate(scopes, c) == nil
}
