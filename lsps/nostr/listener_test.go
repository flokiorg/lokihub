package nostr

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/events"
	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
)

// ---------- test doubles ----------

type mockNostrPool struct {
	calls    atomic.Int32
	builders []func() chan nostr.RelayEvent
}

func (m *mockNostrPool) SubscribeMany(_ context.Context, _ []string, _ nostr.Filter, _ ...nostr.SubscriptionOption) chan nostr.RelayEvent {
	idx := int(m.calls.Add(1)) - 1
	if idx < len(m.builders) {
		return m.builders[idx]()
	}
	return make(chan nostr.RelayEvent)
}

type mockEventPublisher struct {
	publishCalls atomic.Int32
}

func (m *mockEventPublisher) RegisterSubscriber(_ events.EventSubscriber)        {}
func (m *mockEventPublisher) RemoveSubscriber(_ events.EventSubscriber)          {}
func (m *mockEventPublisher) Publish(_ *events.Event)                            { m.publishCalls.Add(1) }
func (m *mockEventPublisher) PublishSync(_ *events.Event)                        { m.publishCalls.Add(1) }
func (m *mockEventPublisher) SetGlobalProperty(_ string, _ interface{})          {}

// ---------- helpers ----------

// newTestListener builds a Listener with just the fields required by runSubscriptionLoop.
func newTestListener(pool nostrPool) *Listener {
	return &Listener{
		pool:              pool,
		relays:            []string{"wss://relay.example.com"},
		stop:              make(chan struct{}),
		getTrustedPubkeys: func() []string { return nil },
	}
}

// closedRelayChannel returns a channel that is already closed.
func closedRelayChannel() chan nostr.RelayEvent {
	ch := make(chan nostr.RelayEvent)
	close(ch)
	return ch
}

// ---------- tests ----------

func TestListener_ReconnectsWhenSubChannelCloses(t *testing.T) {
	var subCount atomic.Int32

	pool := &mockNostrPool{
		builders: []func() chan nostr.RelayEvent{
			func() chan nostr.RelayEvent {
				subCount.Add(1)
				return closedRelayChannel() // closes immediately → triggers backoff + reconnect
			},
			func() chan nostr.RelayEvent {
				subCount.Add(1)
				return make(chan nostr.RelayEvent) // stays open
			},
		},
	}

	l := newTestListener(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go l.runSubscriptionLoop(ctx, nostr.Filter{})

	// resubscribeDelay is 5s, so allow 12s for the second subscribe call.
	assert.Eventually(t, func() bool {
		return subCount.Load() >= 2
	}, 12*time.Second, 20*time.Millisecond, "listener should resubscribe when relay channel closes")
}

func TestListener_StopsOnContextCancel(t *testing.T) {
	pool := &mockNostrPool{
		builders: []func() chan nostr.RelayEvent{
			func() chan nostr.RelayEvent { return make(chan nostr.RelayEvent) },
		},
	}

	l := newTestListener(pool)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		l.runSubscriptionLoop(ctx, nostr.Filter{})
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runSubscriptionLoop did not stop after context cancel")
	}
}

func TestListener_StopsOnStop(t *testing.T) {
	pool := &mockNostrPool{
		builders: []func() chan nostr.RelayEvent{
			func() chan nostr.RelayEvent { return make(chan nostr.RelayEvent) },
		},
	}

	l := newTestListener(pool)
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		l.runSubscriptionLoop(ctx, nostr.Filter{})
		close(done)
	}()

	l.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runSubscriptionLoop did not stop after Stop()")
	}
}

func TestListener_StopsOnContextCancelDuringBackoff(t *testing.T) {
	// First channel closes immediately to trigger the backoff select.
	pool := &mockNostrPool{
		builders: []func() chan nostr.RelayEvent{
			func() chan nostr.RelayEvent { return closedRelayChannel() },
		},
	}

	l := newTestListener(pool)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		l.runSubscriptionLoop(ctx, nostr.Filter{})
		close(done)
	}()

	// Give the goroutine time to enter the backoff select, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runSubscriptionLoop did not exit on ctx cancel during backoff (would have waited full 5s)")
	}
}

func TestListener_StopsOnStopDuringBackoff(t *testing.T) {
	// First channel closes immediately to trigger the backoff select.
	pool := &mockNostrPool{
		builders: []func() chan nostr.RelayEvent{
			func() chan nostr.RelayEvent { return closedRelayChannel() },
		},
	}

	l := newTestListener(pool)
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		l.runSubscriptionLoop(ctx, nostr.Filter{})
		close(done)
	}()

	// Give the goroutine time to enter the backoff select, then stop.
	time.Sleep(50 * time.Millisecond)
	l.Stop()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runSubscriptionLoop did not exit on Stop() during backoff (would have waited full 5s)")
	}
}

// ---------- isTrustedPubkey unit tests ----------

func TestListener_IsTrustedPubkey(t *testing.T) {
	tests := []struct {
		name             string
		getTrustedPubkeys func() []string
		pubkey           string
		want             bool
	}{
		{
			name:             "trusted pubkey returns true",
			getTrustedPubkeys: func() []string { return []string{"aabbcc", "ddeeff"} },
			pubkey:           "aabbcc",
			want:             true,
		},
		{
			name:             "unknown pubkey returns false",
			getTrustedPubkeys: func() []string { return []string{"aabbcc"} },
			pubkey:           "unknown",
			want:             false,
		},
		{
			name:             "empty pubkey returns false",
			getTrustedPubkeys: func() []string { return []string{"aabbcc"} },
			pubkey:           "",
			want:             false,
		},
		{
			name:             "nil getTrustedPubkeys returns false",
			getTrustedPubkeys: nil,
			pubkey:           "aabbcc",
			want:             false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := &Listener{getTrustedPubkeys: tc.getTrustedPubkeys}
			assert.Equal(t, tc.want, l.isTrustedPubkey(tc.pubkey))
		})
	}
}

// ---------- pre-filter test ----------

func TestListener_FiltersUntrustedEventBeforeDispatch(t *testing.T) {
	eventCh := make(chan nostr.RelayEvent, 1)

	pool := &mockNostrPool{
		builders: []func() chan nostr.RelayEvent{
			func() chan nostr.RelayEvent { return eventCh },
		},
	}

	pub := &mockEventPublisher{}
	l := newTestListener(pool)
	l.getTrustedPubkeys = func() []string { return []string{"trusted-pk"} }
	l.eventPublisher = pub

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go l.runSubscriptionLoop(ctx, nostr.Filter{})

	// Send an event from an untrusted pubkey.
	eventCh <- nostr.RelayEvent{Event: &nostr.Event{PubKey: "untrusted-pk"}}

	// Give the goroutine time to process the event.
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, int32(0), pub.publishCalls.Load(),
		"Publish must not be called for events from untrusted sources")
}
