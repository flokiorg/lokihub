package service

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"

	"github.com/ohstr/nmilat/nipcash/transport"
)

// refusalPastE4 reports whether a refused envelope got past E4 (NIP-CASH
// §Serving an Envelope), so its refusal still counts its nonce as served: E4
// itself is the only refusal that leaves a nonce unserved. It repeats E4's
// checks on the plaintext ClassifyEnvelope already refused.
//
// TODO(nmilat review-fixes): use ServeVerdict.Served instead once lokihub pins
// an nmilat that carries it; this mirrors that field for parity-1.
func refusalPastE4(plaintext []byte, limits transport.Limits, now time.Time) bool {
	var raw map[string]json.RawMessage
	if json.Unmarshal(plaintext, &raw) != nil || raw == nil {
		return false
	}
	var nonce string
	if json.Unmarshal(raw["nonce"], &nonce) != nil || !lowerHex64(nonce) {
		return false
	}
	t := bytes.TrimSpace(raw["not_after"])
	if len(t) == 0 || bytes.ContainsAny(t, ".eE\"") {
		return false
	}
	notAfter, err := strconv.ParseInt(string(t), 10, 64)
	if err != nil || notAfter > now.Unix()+limits.MaxRequestAgeSeconds {
		return false
	}
	var items []json.RawMessage
	if json.Unmarshal(raw["items"], &items) != nil || len(items) == 0 {
		return false
	}
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		var item struct {
			ID *string `json:"id"`
		}
		if json.Unmarshal(it, &item) != nil || item.ID == nil {
			continue
		}
		if seen[*item.ID] {
			return false
		}
		seen[*item.ID] = true
	}
	return true
}

func lowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
