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
		case unicode.IsSpace(r), blankish(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r),
			r == 0x2028, r == 0x2029, r == 0xfffd, invisibleish(r):
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

// invisibleish reports characters that render as nothing or as blank space
// without being whitespace: combining marks, variation selectors, Hangul
// fillers, the braille blank and other default-ignorable code points
// (pentest R2-3 MED-4).
func invisibleish(r rune) bool {
	switch {
	case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r):
		return true
	case r == 0x17B4, r == 0x17B5, r == 0x180E, r >= 0xFE00 && r <= 0xFE0F, r >= 0xE0100 && r <= 0xE01EF, r == 0x2060, r == 0xFEFF:
		return true
	}
	return false
}

// confusables maps Cyrillic and Greek letters that look like Latin ones.
var confusables = map[rune]rune{
	'а': 'a', 'в': 'b', 'е': 'e', 'ѕ': 's', 'і': 'i', 'ј': 'j', 'к': 'k', 'м': 'm', 'н': 'h', 'о': 'o',
	'р': 'p', 'с': 'c', 'т': 't', 'у': 'y', 'х': 'x', 'һ': 'h', 'ԁ': 'd', 'ԛ': 'q', 'ԝ': 'w', 'ӏ': 'l',
	'α': 'a', 'β': 'b', 'ε': 'e', 'η': 'n', 'ι': 'i', 'κ': 'k', 'ν': 'v', 'ο': 'o', 'ρ': 'p', 'τ': 't',
	'υ': 'u', 'χ': 'x', 'γ': 'y', 'ω': 'w', 'ɡ': 'g', 'ı': 'i', 'ℓ': 'l', '0': 'o', '1': 'l',
}

// NameSkeleton reduces an assistant name to how it LOOKS: cleaned, lower
// case, lookalike letters folded to Latin, spaces collapsed. Two names with the
// same skeleton are refused as duplicates (pentest R2-3 MED-4).
func NameSkeleton(s string) string {
	s = strings.ToLower(CleanConnectionName(s))
	var b strings.Builder
	for _, r := range s {
		if m, ok := confusables[r]; ok {
			r = m
		}
		b.WriteRune(r)
	}
	return b.String()
}

// blankish: characters that render as an empty gap (Hangul fillers, braille
// blank) — treated as spaces so they can't disguise a duplicate name.
func blankish(r rune) bool {
	return r == 0x115F || r == 0x1160 || r == 0x3164 || r == 0xFFA0 || r == 0x2800
}
