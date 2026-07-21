package controllers

import (
	"sync"
	"time"
)

// RateLimiter is a per-pubkey sliding-window rate limiter. A maxPerHour of 0
// (or less) disables the limit entirely — every call is allowed.
type RateLimiter interface {
	Allow(pubkey string, maxPerHour int) bool
}

type rateLimitEntry struct {
	count   int
	resetAt time.Time
}

// evictEvery bounds how often Allow() sweeps expired entries while already
// holding the lock — every Nth call rather than every call, so eviction cost
// is amortized instead of paid on every request.
const evictEvery = 100

type pubkeyRateLimiter struct {
	mu        sync.Mutex
	entries   map[string]*rateLimitEntry
	callsSeen uint64
}

// NewRateLimiter returns a new in-memory per-pubkey rate limiter.
func NewRateLimiter() RateLimiter {
	return &pubkeyRateLimiter{
		entries: make(map[string]*rateLimitEntry),
	}
}

func (rl *pubkeyRateLimiter) Allow(pubkey string, maxPerHour int) bool {
	if maxPerHour <= 0 {
		return true
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	rl.callsSeen++
	if rl.callsSeen%evictEvery == 0 {
		rl.evictExpiredLocked(now)
	}

	entry, ok := rl.entries[pubkey]
	if !ok || now.After(entry.resetAt) {
		rl.entries[pubkey] = &rateLimitEntry{
			count:   1,
			resetAt: now.Add(time.Hour),
		}
		return true
	}
	if entry.count >= maxPerHour {
		return false
	}
	entry.count++
	return true
}

// evictExpiredLocked removes entries whose window has already reset, so the
// map stays bounded to pubkeys with recent activity instead of growing for
// the lifetime of the process. Must be called with rl.mu held.
func (rl *pubkeyRateLimiter) evictExpiredLocked(now time.Time) {
	for pubkey, entry := range rl.entries {
		if now.After(entry.resetAt) {
			delete(rl.entries, pubkey)
		}
	}
}
