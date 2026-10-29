package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ohstr/nmilat/nipcash/transport"
)

// envelopeHeaderKeys are the envelope's own keys (NIP-CASH §Envelopes).
var envelopeHeaderKeys = [...]string{"nonce", "reply_to", "not_after", "items"}

// checkEnvelopeHeader confirms that env, decoded case-insensitively, is the
// envelope its exact keys say: an envelope carrying a case variant of a header
// key could be checked on one value and run on another, so it is dropped. An
// envelope past E4 (served) also has its not_after bounded by max_request_age
// here, as E4 requires.
func checkEnvelopeHeader(plaintext []byte, env *transport.Envelope, served bool, limits transport.Limits, now time.Time) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(plaintext, &raw); err != nil {
		return fmt.Errorf("envelope does not parse: %w", err)
	}
	for k := range raw {
		for _, want := range envelopeHeaderKeys {
			if k != want && strings.EqualFold(k, want) {
				return fmt.Errorf("envelope carries %q beside its %q key", k, want)
			}
		}
	}
	if served && env.NotAfter > now.Unix()+limits.MaxRequestAgeSeconds {
		return fmt.Errorf("not_after %d is more than %d s ahead", env.NotAfter, limits.MaxRequestAgeSeconds)
	}
	return nil
}
