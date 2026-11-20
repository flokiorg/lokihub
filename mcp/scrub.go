package mcp

import (
	"encoding/json"
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
			unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r) || r == 0xfffd) {
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
		case r == '\t':
			b.WriteByte(' ')
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f,
			unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r), r == 0xfffd:
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// scrubOutput applies untrustedText to every string in a tool's output, at any
// depth, including inside opaque maps (network graph, metadata). It works on
// the JSON form so it covers every output type uniformly; if a value does not
// round-trip it is returned unchanged.
func scrubOutput[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var tree any
	if json.Unmarshal(b, &tree) != nil {
		return v
	}
	changed := false
	tree = scrubTree(tree, &changed)
	if !changed {
		return v
	}
	nb, err := json.Marshal(tree)
	if err != nil {
		return v
	}
	var out T
	if json.Unmarshal(nb, &out) != nil {
		return v
	}
	return out
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
		for k, x := range t {
			t[k] = scrubTree(x, changed)
		}
		return t
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
