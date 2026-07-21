package controllers

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRateLimiter_AllowsUpToMax(t *testing.T) {
	rl := NewRateLimiter().(*pubkeyRateLimiter)

	for i := 0; i < 3; i++ {
		assert.True(t, rl.Allow("pubkey1", 3))
	}
	assert.False(t, rl.Allow("pubkey1", 3), "4th call within the window must be denied")
}

func TestRateLimiter_ResetsAfterWindow(t *testing.T) {
	rl := NewRateLimiter().(*pubkeyRateLimiter)

	rl.entries["pubkey1"] = &rateLimitEntry{count: 5, resetAt: time.Now().Add(-time.Minute)}
	assert.True(t, rl.Allow("pubkey1", 3), "an expired window must reset the count")
}

// TestRateLimiter_EvictsExpiredEntries verifies the map doesn't grow forever:
// once enough calls have been made to trigger a sweep, entries whose window
// has already elapsed are removed rather than retained indefinitely.
func TestRateLimiter_EvictsExpiredEntries(t *testing.T) {
	rl := NewRateLimiter().(*pubkeyRateLimiter)

	// Seed with an already-expired entry.
	rl.entries["stale-pubkey"] = &rateLimitEntry{count: 1, resetAt: time.Now().Add(-time.Hour)}
	require := assert.New(t)
	require.Len(rl.entries, 1)

	// Drive enough calls (with distinct pubkeys) to cross the eviction threshold.
	for i := 0; i < evictEvery; i++ {
		rl.Allow(fmt.Sprintf("active-pubkey-%d", i), 100)
	}

	rl.mu.Lock()
	_, staleStillPresent := rl.entries["stale-pubkey"]
	rl.mu.Unlock()
	require.False(staleStillPresent, "expired entry must be evicted once the sweep threshold is reached")
}
