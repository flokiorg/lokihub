package apps

import (
	"crypto/rand"
	"encoding/hex"
)

// maxChildAppNameLen caps the display name of an auto-generated child app (a
// JIT wallet or Circle wallet spawned under a hub) so a long hub name can't
// produce an unwieldy label in app lists.
const maxChildAppNameLen = 48

// childNameRandomLen is the number of hex characters appended to a generated
// child name so wallets stay visually distinct even when their identity
// label repeats (e.g. requesters sharing a pubkey prefix, or no identity
// label at all).
const childNameRandomLen = 4

// GenerateChildName builds a display name for a child app spawned under a
// parent hub: "<hub name> · <identity label> · <random>". identityLabel is
// typically a prefix of the requester's pubkey or connection_key; callers may
// pass "" when no identity is available yet, in which case that segment is
// omitted. The hub name is trimmed (not the identity label or random suffix)
// when the combined result would exceed maxChildAppNameLen, since the label
// and suffix are what keep sibling wallets distinguishable.
func GenerateChildName(hubName, identityLabel string) string {
	if len(identityLabel) > 8 {
		identityLabel = identityLabel[:8]
	}

	tail := " · " + randomHex(childNameRandomLen)
	if identityLabel != "" {
		tail = " · " + identityLabel + tail
	}

	hubRunes := []rune(hubName)
	maxHubRunes := maxChildAppNameLen - len([]rune(tail))
	if maxHubRunes < 1 {
		maxHubRunes = 1
	}
	if len(hubRunes) > maxHubRunes {
		hubRunes = hubRunes[:maxHubRunes]
	}

	return string(hubRunes) + tail
}

// randomHex returns n lowercase hex characters read from crypto/rand.
func randomHex(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand only fails if the OS entropy source is unavailable;
		// fall back to a fixed suffix rather than blocking wallet creation.
		return "0000"[:n]
	}
	return hex.EncodeToString(b)[:n]
}
