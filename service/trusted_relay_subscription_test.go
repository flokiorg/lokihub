package service

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"

	"github.com/flokiorg/lokihub/nip47/models"
)

func knownWalletPubkey() string   { return strings.Repeat("a", walletPubkeyHexLen) }
func foreignWalletPubkey() string { return strings.Repeat("b", walletPubkeyHexLen) }

func newGateService(t *testing.T) *service {
	t.Helper()
	svc := &service{walletRegistry: newWalletRegistry()}
	svc.walletRegistry.Add(knownWalletPubkey())
	return svc
}

func requestEvent(kind int, tags nostr.Tags) *nostr.Event {
	return &nostr.Event{Kind: kind, Tags: tags}
}

// TestAcceptsRequestEvent is the gate that replaced the relay's own "p"
// filtering. Anything it lets through reaches app lookup and decryption, and
// anything it wrongly rejects is a request a real wallet never gets — so both
// directions are pinned here.
func TestAcceptsRequestEvent(t *testing.T) {
	known := knownWalletPubkey()

	tests := []struct {
		name  string
		event *nostr.Event
		want  bool
	}{
		{
			name:  "request for a wallet we serve",
			event: requestEvent(models.REQUEST_KIND, nostr.Tags{{"p", known}}),
			want:  true,
		},
		{
			name:  "extra tags before the p tag",
			event: requestEvent(models.REQUEST_KIND, nostr.Tags{{"e", "something"}, {"p", known}}),
			want:  true,
		},
		{
			name:  "request for another hub's wallet",
			event: requestEvent(models.REQUEST_KIND, nostr.Tags{{"p", foreignWalletPubkey()}}),
			want:  false,
		},
		{
			name:  "wrong kind",
			event: requestEvent(1, nostr.Tags{{"p", known}}),
			want:  false,
		},
		{
			name:  "no p tag",
			event: requestEvent(models.REQUEST_KIND, nostr.Tags{{"e", "something"}}),
			want:  false,
		},
		{
			name:  "p tag with no value",
			event: requestEvent(models.REQUEST_KIND, nostr.Tags{{"p"}}),
			want:  false,
		},
		{
			name:  "short p tag",
			event: requestEvent(models.REQUEST_KIND, nostr.Tags{{"p", "aa"}}),
			want:  false,
		},
		{
			name:  "overlong p tag",
			event: requestEvent(models.REQUEST_KIND, nostr.Tags{{"p", strings.Repeat("a", 65)}}),
			want:  false,
		},
		{
			name:  "no tags at all",
			event: requestEvent(models.REQUEST_KIND, nostr.Tags{}),
			want:  false,
		},
		{
			name:  "nil event",
			event: nil,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newGateService(t)
			assert.Equal(t, tt.want, svc.acceptsRequestEvent(tt.event))
		})
	}
}

// TestAcceptsRequestEvent_CountsDrops: the drop count is the only signal an
// operator gets that junk is arriving, since the gate never logs per event.
func TestAcceptsRequestEvent_CountsDrops(t *testing.T) {
	svc := newGateService(t)

	assert.True(t, svc.acceptsRequestEvent(requestEvent(models.REQUEST_KIND, nostr.Tags{{"p", knownWalletPubkey()}})))
	assert.Zero(t, svc.droppedRequestEvents.Load(), "an accepted event must not count as dropped")

	svc.acceptsRequestEvent(requestEvent(models.REQUEST_KIND, nostr.Tags{{"p", foreignWalletPubkey()}}))
	svc.acceptsRequestEvent(requestEvent(1, nostr.Tags{{"p", knownWalletPubkey()}}))
	svc.acceptsRequestEvent(requestEvent(models.REQUEST_KIND, nostr.Tags{}))

	assert.EqualValues(t, 3, svc.droppedRequestEvents.Load())
}

// TestAcceptsRequestEvent_FollowsRegistry: a wallet stops being served the
// moment its app is deleted, and starts being served as soon as one is
// created — the latter is what removes the create-then-publish race.
func TestAcceptsRequestEvent_FollowsRegistry(t *testing.T) {
	svc := newGateService(t)
	fresh := foreignWalletPubkey()
	event := requestEvent(models.REQUEST_KIND, nostr.Tags{{"p", fresh}})

	assert.False(t, svc.acceptsRequestEvent(event), "unknown before the app exists")

	svc.walletRegistry.Add(fresh)
	assert.True(t, svc.acceptsRequestEvent(event), "served as soon as the app is registered")

	svc.walletRegistry.Remove(fresh)
	assert.False(t, svc.acceptsRequestEvent(event), "no longer served once the app is deleted")
}

func TestFirstPTagValue(t *testing.T) {
	assert.Equal(t, "value", firstPTagValue(requestEvent(1, nostr.Tags{{"p", "value"}})))
	assert.Equal(t, "first", firstPTagValue(requestEvent(1, nostr.Tags{{"p", "first"}, {"p", "second"}})))
	assert.Equal(t, "", firstPTagValue(requestEvent(1, nostr.Tags{{"e", "x"}})))
	assert.Equal(t, "", firstPTagValue(requestEvent(1, nostr.Tags{})))
}

// BenchmarkAcceptsRequestEvent_Rejects is the path an attacker drives: every
// junk event on the relay reaches it. It must stay allocation-free.
func BenchmarkAcceptsRequestEvent_Rejects(b *testing.B) {
	svc := &service{walletRegistry: newWalletRegistry()}
	svc.walletRegistry.Add(strings.Repeat("a", walletPubkeyHexLen))
	event := requestEvent(models.REQUEST_KIND, nostr.Tags{{"p", strings.Repeat("b", walletPubkeyHexLen)}})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		svc.acceptsRequestEvent(event)
	}
}
