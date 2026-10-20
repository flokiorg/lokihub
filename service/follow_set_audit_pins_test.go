package service

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
)

// NIP-01 pubkeys are lowercase hex, and joins only accept lowercase. A `p` tag
// in uppercase (or as an npub) can never match a requester, so it is dropped
// rather than counted: the member it meant is refused until the provider fixes
// the tag. Pinned so the set's member count never includes it.
func TestParseFollowSet_NonCanonicalPTagsAreNotMembers(t *testing.T) {
	providerSK, _ := newKeypair(t)
	_, member := newKeypair(t)

	evt := signedFollowSetEvent(t, providerSK, testD, nil,
		nostr.Tag{"p", strings.ToUpper(member)},
		nostr.Tag{"p", "npub1notahexkey"},
		nostr.Tag{"p", member[:63]},
	)

	set := parseFollowSet(&evt, testD, "")
	assert.Zero(t, set.MemberCount())
	assert.False(t, set.IsMember(member))
	assert.False(t, set.IsMember(strings.ToUpper(member)))
}
