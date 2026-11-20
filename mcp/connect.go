package mcp

import (
	"errors"
	"fmt"
	"golang.org/x/text/unicode/norm"
	"sort"
	"strings"
	"time"
	"unicode"
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
	name = CleanConnectionName(name)
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

// CleanConnectionName canonicalises an assistant's display name so two names
// that LOOK the same are the same string (pentest E MED-2): NFKC-normalised,
// invisible / direction-changing / control characters dropped, whitespace
// collapsed, capped at 64 characters. The core's duplicate-name check then
// catches lookalikes ("Desk assistant" + zero-width space).
func CleanConnectionName(s string) string {
	s = norm.NFKC.String(s)
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r),
			r == 0x2028, r == 0x2029, r == 0xfffd:
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if r := []rune(out); len(r) > 64 {
		out = string(r[:64])
	}
	return strings.TrimSpace(out)
}
