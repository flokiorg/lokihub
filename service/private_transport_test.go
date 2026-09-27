package service

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testInboxXOnly = "aa11223344556677889900aabbccddeeff00112233445566778899aabbccddee"
	testNodeXOnly  = "bb11223344556677889900aabbccddeeff00112233445566778899aabbccddee"
)

func newTestPrivateTransport() *privateTransport {
	return &privateTransport{inboxXOnly: testInboxXOnly, nodeXOnly: testNodeXOnly}
}

func privateEvent(pTag, content string) *nostr.Event {
	return &nostr.Event{
		Kind:    transport.KindPrivateRequest,
		Tags:    nostr.Tags{nostr.Tag{"p", pTag}},
		Content: content,
	}
}

var validWrapContent = strings.Repeat("x", minWrapBytes+10)

func TestAcceptsPrivateEvent_AcceptsAWellFormedEnvelope(t *testing.T) {
	pt := newTestPrivateTransport()
	assert.True(t, pt.acceptsPrivateEvent(privateEvent(testInboxXOnly, validWrapContent)))
	assert.Zero(t, pt.droppedEvents.Load(), "an accepted event must not be counted as dropped")
}

// TestAcceptsPrivateEvent_Rejects covers what the gate can cheaply refuse. It is a
// deliberately short list: the inbox key is public, so anyone can address a
// well-formed event to it, and any test the hub could apply without its private key
// an attacker can satisfy. Everything past this point costs an ECDH.
func TestAcceptsPrivateEvent_Rejects(t *testing.T) {
	tests := map[string]*nostr.Event{
		"nil event":       nil,
		"wrong kind":      {Kind: 23194, Tags: nostr.Tags{nostr.Tag{"p", testInboxXOnly}}, Content: validWrapContent},
		"no p tag":        {Kind: transport.KindPrivateRequest, Content: validWrapContent},
		"another hub's p": privateEvent(strings.Repeat("cc", 32), validWrapContent),
		// The node identity is public and an obvious thing to aim at, but it is
		// not the inbox — the hub cannot decrypt to it.
		"node key as p":     privateEvent(testNodeXOnly, validWrapContent),
		"content too short": privateEvent(testInboxXOnly, "tiny"),
		"empty content":     privateEvent(testInboxXOnly, ""),
	}

	for name, event := range tests {
		t.Run(name, func(t *testing.T) {
			pt := newTestPrivateTransport()
			assert.False(t, pt.acceptsPrivateEvent(event))
			assert.Equal(t, int64(1), pt.droppedEvents.Load(), "a rejected event must be counted")
		})
	}
}

// TestAcceptsPrivateEvent_IsAllocationFree matters because this runs for every
// event reaching the hub, including everything an attacker sends. Allocating here
// would hand them a way to drive the garbage collector.
func TestAcceptsPrivateEvent_IsAllocationFree(t *testing.T) {
	pt := newTestPrivateTransport()
	accepted := privateEvent(testInboxXOnly, validWrapContent)
	rejected := privateEvent(strings.Repeat("cc", 32), validWrapContent)

	allocs := testing.AllocsPerRun(100, func() {
		pt.acceptsPrivateEvent(accepted)
		pt.acceptsPrivateEvent(rejected)
	})
	assert.Zero(t, allocs, "the wire gate allocated %v times per run", allocs)
}

// TestAcceptsPrivateEvent_CountsEveryDrop pins the operator signal. Drops are
// summarised on a timer rather than logged per event — logging on a path an
// attacker drives is itself a denial of service — so the counter is the only
// evidence a flood is happening.
func TestAcceptsPrivateEvent_CountsEveryDrop(t *testing.T) {
	pt := newTestPrivateTransport()
	for i := 0; i < 5; i++ {
		pt.acceptsPrivateEvent(privateEvent(strings.Repeat("cc", 32), validWrapContent))
	}
	require.Equal(t, int64(5), pt.droppedEvents.Load())

	// Swap is what the periodic logger uses; it must reset so each interval
	// reports its own count rather than a running total.
	assert.Equal(t, int64(5), pt.droppedEvents.Swap(0))
	assert.Zero(t, pt.droppedEvents.Load())
}

// TestPrivateTransport_InboxIsNotTheNodeKey pins the invariant that makes the whole
// scheme work. The node key signs the announcement; the inbox key opens the mail.
// If they were ever the same, the hub could not decrypt anything sent to it — the
// node will not perform NIP-44 ECDH — and every client would publish into a void,
// which presents as silence rather than an error.
func TestPrivateTransport_InboxIsNotTheNodeKey(t *testing.T) {
	pt := newTestPrivateTransport()
	require.NotEqual(t, pt.inboxXOnly, pt.nodeXOnly)

	// And an event aimed at the node key is refused, since that is a plausible
	// mistake for a client to make.
	assert.False(t, pt.acceptsPrivateEvent(privateEvent(pt.nodeXOnly, validWrapContent)))
}
