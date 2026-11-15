package notifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withCleanupInterval temporarily overrides the package-level CleanupInterval
// var so tests don't have to wait a real hour between ticks, restoring it on
// cleanup. Not safe to run in parallel with other tests that touch it.
func withCleanupInterval(t *testing.T, d time.Duration) {
	t.Helper()
	orig := CleanupInterval
	CleanupInterval = d
	t.Cleanup(func() { CleanupInterval = orig })
}

func TestCleanupService_RemovesExpiredImmediatelyOnStart(t *testing.T) {
	withCleanupInterval(t, time.Hour)
	store := newFakeNotifStore()
	c := NewCleanupService(store)
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan struct{})
	go func() {
		c.Start(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return store.removeExpiredCallCount() >= 1
	}, time.Second, 10*time.Millisecond, "must clean up on start, not only after the first interval elapses")

	cancel()
	<-done
}

func TestCleanupService_PassesExpiryDurationCutoff(t *testing.T) {
	withCleanupInterval(t, time.Hour)
	store := newFakeNotifStore()
	c := NewCleanupService(store)
	ctx, cancel := context.WithCancel(t.Context())

	before := time.Now()
	done := make(chan struct{})
	go func() {
		c.Start(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return store.removeExpiredCallCount() >= 1
	}, time.Second, 10*time.Millisecond)
	cancel()
	<-done

	wantCutoff := before.Add(-ExpiryDuration)
	gotCutoff := store.lastRemoveExpiredBefore()
	assert.WithinDuration(t, wantCutoff, gotCutoff, 5*time.Second,
		"RemoveExpired's cutoff must be now-ExpiryDuration, not e.g. now or the zero time")
}

func TestCleanupService_RunsPeriodically(t *testing.T) {
	withCleanupInterval(t, 20*time.Millisecond)
	store := newFakeNotifStore()
	c := NewCleanupService(store)
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan struct{})
	go func() {
		c.Start(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return store.removeExpiredCallCount() >= 3
	}, time.Second, 10*time.Millisecond, "must keep cleaning up on every tick, not just once")

	// Stop the loop and wait for its goroutine to actually exit before this
	// test returns — otherwise t.Cleanup restoring the package-level
	// CleanupInterval var can race the loop's next read of it.
	cancel()
	<-done
}

func TestCleanupService_StopsOnContextCancel(t *testing.T) {
	withCleanupInterval(t, time.Hour)
	store := newFakeNotifStore()
	c := NewCleanupService(store)
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan struct{})
	go func() {
		c.Start(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return store.removeExpiredCallCount() >= 1
	}, time.Second, 10*time.Millisecond)

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Start must return promptly once its context is cancelled, even mid-interval-wait")
	}
}

func TestCleanupService_StoreErrorDoesNotStopTheLoop(t *testing.T) {
	withCleanupInterval(t, 20*time.Millisecond)
	store := newFakeNotifStore()
	store.removeExpiredErr = errors.New("db unavailable")
	c := NewCleanupService(store)
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan struct{})
	go func() {
		c.Start(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return store.removeExpiredCallCount() >= 3
	}, time.Second, 10*time.Millisecond, "a store error on one tick must not kill the cleanup loop")

	cancel()
	<-done
}
