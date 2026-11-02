package lsps

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPeerBurstLimiter_AllowsUpToLimitThenRejects(t *testing.T) {
	l := newPeerBurstLimiter(3, time.Minute)
	peer := "peerA"
	assert.True(t, l.Allow(peer))
	assert.True(t, l.Allow(peer))
	assert.True(t, l.Allow(peer))
	assert.False(t, l.Allow(peer), "a 4th call within the same window must be rejected")
}

func TestPeerBurstLimiter_DifferentPeersHaveIndependentWindows(t *testing.T) {
	l := newPeerBurstLimiter(1, time.Minute)
	assert.True(t, l.Allow("peerA"))
	assert.False(t, l.Allow("peerA"), "peerA already used its one allowance")
	assert.True(t, l.Allow("peerB"), "peerB must not be affected by peerA's window")
}

// TestPeerBurstLimiter_WindowResetsAfterExpiry forces the window to have
// already ended (instead of sleeping in a test) to prove a peer's count
// resets on the next call after its window lapses.
func TestPeerBurstLimiter_WindowResetsAfterExpiry(t *testing.T) {
	l := newPeerBurstLimiter(1, time.Minute)
	assert.True(t, l.Allow("peerA"))
	assert.False(t, l.Allow("peerA"))

	l.mu.Lock()
	l.seen["peerA"].endsAt = time.Now().Add(-time.Second)
	l.mu.Unlock()

	assert.True(t, l.Allow("peerA"), "a new window must reset the count")
}

func TestPeerBurstLimiter_EvictExpired_RemovesOnlyExpiredEntries(t *testing.T) {
	l := newPeerBurstLimiter(5, time.Minute)
	l.Allow("stale")
	l.Allow("fresh")

	l.mu.Lock()
	l.seen["stale"].endsAt = time.Now().Add(-time.Second)
	l.mu.Unlock()

	l.EvictExpired()

	l.mu.Lock()
	_, staleExists := l.seen["stale"]
	_, freshExists := l.seen["fresh"]
	l.mu.Unlock()

	assert.False(t, staleExists, "an expired entry must be evicted")
	assert.True(t, freshExists, "a live entry must survive eviction")
}

// TestPeerBurstLimiter_NilReceiver_AlwaysAllows covers a *Handlers built
// via a bare struct literal (common in this package's table-driven tests)
// instead of NewHandlers, which leaves burstLimiter nil -- must behave as
// "no burst shaping configured," never panic.
func TestPeerBurstLimiter_NilReceiver_AlwaysAllows(t *testing.T) {
	var l *peerBurstLimiter
	assert.True(t, l.Allow("anyone"))
}

// TestPeerBurstLimiter_NilReceiver_EvictExpiredAndStartEvictionLoopDoNotPanic
// is the regression test for the doc/API inconsistency: Allow was made
// nil-receiver-safe, but EvictExpired/StartEvictionLoop were not, despite
// the type's own doc comment claiming the whole type is "nil-safe by
// convention." Both must now be no-ops on a nil receiver, not panic.
func TestPeerBurstLimiter_NilReceiver_EvictExpiredAndStartEvictionLoopDoNotPanic(t *testing.T) {
	var l *peerBurstLimiter
	assert.NotPanics(t, func() { l.EvictExpired() })

	stopCh := make(chan struct{})
	done := make(chan struct{})
	assert.NotPanics(t, func() {
		go func() {
			l.StartEvictionLoop(stopCh, time.Millisecond)
			close(done)
		}()
	})
	time.Sleep(5 * time.Millisecond) // let at least one tick fire EvictExpired on the nil receiver
	close(stopCh)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("StartEvictionLoop on a nil receiver did not stop promptly after stopCh was closed")
	}
}

func TestPeerBurstLimiter_ConcurrentAccess_RespectsLimitExactly(t *testing.T) {
	l := newPeerBurstLimiter(50, time.Minute)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow("peerA") {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 50, allowed, "exactly `limit` concurrent calls should be allowed, no more, no fewer")
}

func TestPeerBurstLimiter_StartEvictionLoop_StopsOnSignal(t *testing.T) {
	l := newPeerBurstLimiter(5, time.Minute)
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		l.StartEvictionLoop(stopCh, time.Hour)
		close(done)
	}()

	close(stopCh)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("StartEvictionLoop did not stop promptly after stopCh was closed")
	}
}
