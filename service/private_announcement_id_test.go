package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ohstr/nmilat/nipcash/transport"
)

// TestAnnouncementID_AgreesWithGoNostr is the regression guard for a bug that no
// in-process test could see, and that a live relay caught immediately.
//
// The hub built its kind-11190 announcement with nmilat, signed it with the node
// key, and verified it with nmilat's own ParseAnnouncement — which passed. The relay
// then refused it: "invalid: event ID mismatch". nmilat's MarshalTags handed a nil
// tag slice to encoding/json, which renders it as `null`, while NIP-01's positional
// serialization requires `[]`. Every part of the local pipeline agreed, because every
// part hashed the same wrong digest.
//
// This test exists because this repo is the only place both implementations meet.
// nmilat computes the id; go-nostr is what actually publishes, and computes the same
// id the way a relay does. Comparing them is the whole point — either alone is
// self-consistent and proves nothing.
//
// The announcement is specifically the event worth pinning: it is the only one this
// system builds with NO tags, since it is addressed by author alone (a replaceable
// kind plus one author returns exactly one event), so it is the only event that can
// expose a nil-tags serialization bug.
func TestAnnouncementID_AgreesWithGoNostr(t *testing.T) {
	ev, err := transport.NewAnnouncement(
		"c9a6e09e2e9d1529c87c034891e05ddf32343fad7a9af563b9de6958c95f5990",
		"66e016a38c70ef4c9d511aceb74406e96e86fde6f797a35825c32d79cb086d5b",
		transport.DefaultLimits(),
		[]string{"ws://relay.example:5500"},
	)
	require.NoError(t, err)
	require.Empty(t, ev.Tags, "the announcement is expected to carry no tags — that is what makes it the event worth testing here")

	published, err := toGoNostrEvent(ev)
	require.NoError(t, err)

	// GetID() recomputes from the serialization, exactly as a relay does, rather
	// than echoing the ID field toGoNostrEvent copied across.
	assert.Equal(t, ev.ID, published.GetID(),
		"nmilat's event id disagrees with go-nostr's; a relay will refuse this event with an id mismatch")
}

// TestToGoNostrEvent_EmptyTagsSurviveTheConversion guards the other half of the same
// hazard: the conversion must not turn an absent tag list into something that
// serializes differently, since the id was already computed over the original.
func TestToGoNostrEvent_EmptyTagsSurviveTheConversion(t *testing.T) {
	ev, err := transport.NewAnnouncement(
		"c9a6e09e2e9d1529c87c034891e05ddf32343fad7a9af563b9de6958c95f5990",
		"66e016a38c70ef4c9d511aceb74406e96e86fde6f797a35825c32d79cb086d5b",
		transport.DefaultLimits(),
		nil,
	)
	require.NoError(t, err)

	published, err := toGoNostrEvent(ev)
	require.NoError(t, err)
	assert.NotNil(t, published.Tags, "go-nostr serializes nil Tags as null; it must be a non-nil empty slice")
	assert.Empty(t, published.Tags)
	assert.Equal(t, ev.ID, published.GetID())

	// And the serialization itself must carry [] in the tags position — the exact
	// byte-level fact the relay's own hash depends on.
	assert.Contains(t, string(published.Serialize()), ",[],",
		"the tags position must serialize as [] — null there yields a different digest than every relay computes")
}
