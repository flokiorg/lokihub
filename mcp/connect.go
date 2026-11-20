package mcp

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// ErrNoGrants is returned when a connection is requested with no capability
// group granted — an assistant that can do nothing is a configuration error.
var ErrNoGrants = errors.New("mcp: at least one capability group must be granted")

// ScopesFor maps the owner's per-group grants (Off = absent) to mcp:* scopes. A
// Read+Write grant (ModeWrite) holds both the :read and :write scope, so write
// access always implies read access within a group.
func ScopesFor(grants map[Group]Mode) ([]string, error) {
	if len(grants) == 0 {
		return nil, ErrNoGrants
	}
	var out []string
	for g, m := range grants {
		if !g.Valid() {
			return nil, fmt.Errorf("mcp: unknown capability group %q", g)
		}
		switch m {
		case ModeRead:
			out = append(out, g.Scope(ModeRead))
		case ModeWrite:
			out = append(out, g.Scope(ModeRead), g.Scope(ModeWrite))
		default:
			return nil, fmt.Errorf("mcp: invalid mode %q for group %q", m, g)
		}
	}
	sort.Strings(out)
	return out, nil
}

// AppCreator persists an MCP App (kind AppKindMCP) with the given scopes and
// returns its id. The hub wires it to apps.AppsService.CreateApp.
type AppCreator func(name string, expiresAt *time.Time, scopes []string) (appID uint, err error)

// CreateConnection mints a new AI-assistant connection: it creates the App with
// the granted scopes and returns a bearer token for it. The token is shown to
// the owner once; the hub never stores it (it is re-derivable only with the
// hub secret, and deleting the App revokes it).
func CreateConnection(create AppCreator, tokens *HMACTokenStore, name string, grants map[Group]Mode, expiresAt *time.Time) (uint, string, error) {
	if name == "" {
		return 0, "", errors.New("mcp: connection name required")
	}
	scopes, err := ScopesFor(grants)
	if err != nil {
		return 0, "", err
	}
	appID, err := create(name, expiresAt, scopes)
	if err != nil {
		return 0, "", err
	}
	tok, err := tokens.Mint(appID)
	if err != nil {
		return 0, "", err
	}
	return appID, tok, nil
}

// GrantsFromScopes is the inverse of ScopesFor: it reconstructs the per-group
// grant (ModeWrite if the group's :write scope is held, else ModeRead) from a
// connection's stored scopes, ignoring anything that is not an mcp:* scope.
func GrantsFromScopes(scopes []string) map[Group]Mode {
	out := map[Group]Mode{}
	for _, g := range AllGroups {
		hasR, hasW := false, false
		for _, s := range scopes {
			switch s {
			case g.Scope(ModeRead):
				hasR = true
			case g.Scope(ModeWrite):
				hasW = true
			}
		}
		switch {
		case hasW:
			out[g] = ModeWrite
		case hasR:
			out[g] = ModeRead
		}
	}
	return out
}
