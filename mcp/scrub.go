package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
)

// untrustedText neutralises text that may come from a third party (invoice
// memos, names, aliases, relay content) before an assistant sees it (pentest C
// F1/F5): control, invisible, private-use and direction-changing characters are
// removed (they hide or re-order what is shown), and line breaks become a
// visible "⏎" so injected text cannot pose as new lines of instructions.
// Plain ASCII — invoices, keys, addresses, ids — passes through unchanged.
func untrustedText(s string) string {
	clean := true
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r >= 0x80 && (r <= 0x9f || r == 0x2028 || r == 0x2029 ||
			unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r) || r == 0xfffd || hiddenCarrier(r) || blankish(r)) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n', r == '\r', r == 0x2028, r == 0x2029:
			b.WriteString(" ⏎ ")
		case r == '\t', blankish(r):
			b.WriteByte(' ')
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f,
			unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r), r == 0xfffd, hiddenCarrier(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ErrUnscrubbable — an output could not be checked for hidden or line-forging
// characters, so it is withheld rather than passed through unchecked.
var ErrUnscrubbable = errors.New("mcp: this result could not be safely prepared for the assistant")

// scrubOutput applies untrustedText to every string (values and map keys) in a
// tool's output, at any depth, including inside opaque maps. It works on the
// JSON form with exact numbers (UseNumber), and FAILS CLOSED: an output that
// cannot round-trip is withheld, never returned unscrubbed (pentest R2 M1).
func scrubOutput[T any](v T) (T, error) {
	var zero T
	b, err := json.Marshal(v)
	if err != nil {
		return zero, ErrUnscrubbable
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var tree any
	if dec.Decode(&tree) != nil {
		return zero, ErrUnscrubbable
	}
	changed := false
	tree = scrubTree(tree, &changed)
	if !changed {
		return v, nil
	}
	nb, err := json.Marshal(tree)
	if err != nil {
		return zero, ErrUnscrubbable
	}
	var out T
	if json.Unmarshal(nb, &out) != nil {
		return zero, ErrUnscrubbable
	}
	return out, nil
}

func scrubTree(v any, changed *bool) any {
	switch t := v.(type) {
	case string:
		if c := untrustedText(t); c != t {
			*changed = true
			return c
		}
		return t
	case []any:
		for i := range t {
			t[i] = scrubTree(t[i], changed)
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			ck := untrustedText(k)
			if ck != k {
				*changed = true
			}
			out[ck] = scrubTree(x, changed)
		}
		return out
	}
	return v
}

// toolErr scrubs an error's text before the assistant sees it (core and LSP
// error messages can carry third-party text — pentest C F7); errors.Is/As
// still see the original.
func toolErr(err error) error {
	if err == nil {
		return nil
	}
	if c := untrustedText(err.Error()); c != err.Error() {
		return scrubbedErr{msg: c, err: err}
	}
	return err
}

type scrubbedErr struct {
	msg string
	err error
}

func (e scrubbedErr) Error() string { return e.msg }
func (e scrubbedErr) Unwrap() error { return e.err }

// hiddenCarrier reports invisible code points that can smuggle a hidden
// message inside visible text (pentest R2-4 MED-2): variation selectors (a
// sequence of them encodes arbitrary bytes), the combining grapheme joiner,
// Mongolian vowel separator, unassigned code points and non-characters.
// Ordinary combining marks are kept — many scripts need them.
func hiddenCarrier(r rune) bool {
	switch {
	case r >= 0xFE00 && r <= 0xFE0F, r >= 0xE0100 && r <= 0xE01EF, r == 0x034F, r == 0x180E:
		return true
	case r >= 0xFDD0 && r <= 0xFDEF, r&0xFFFE == 0xFFFE:
		return true
	case !unicode.Is(unicode.L, r) && !unicode.Is(unicode.M, r) && !unicode.Is(unicode.N, r) &&
		!unicode.Is(unicode.P, r) && !unicode.Is(unicode.S, r) && !unicode.Is(unicode.Z, r) &&
		!unicode.Is(unicode.C, r):
		return true // unassigned (Cn)
	}
	return false
}
