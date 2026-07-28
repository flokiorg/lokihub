package lokicash

import "testing"

// FuzzDecode checks lokicash.Decode never panics on adversarial input.
// NOTE: Decode has no server-side ingestion path (the hub only Encodes tokens;
// independent audit, 2026-07-28), so this is defense-in-depth on the
// client decoder, not a reachable server attack surface.
func FuzzDecode(f *testing.F) {
	f.Add("lokicash1")
	f.Add("lokicash1qqqq")
	f.Add("")
	f.Add("satscash1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq")
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = Decode(s) // must not panic
	})
}
