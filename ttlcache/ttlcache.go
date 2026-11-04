// Package ttlcache is a small cache of computed results, each reused for at most
// a fixed time and dropped at once when what it shows changes.
//
// It is safe against a classic race: a result computed before an invalidation is
// never stored after it. Begin stamps a computation; Put drops a result whose
// stamp an Invalidate has passed since.
//
// A nil *Cache is a cache that holds nothing: Get misses, Put stores nothing and
// Invalidate does nothing. Code that takes an optional cache needs no nil checks.
package ttlcache

import (
	"sync"
	"time"
)

type entry struct {
	at     time.Time
	result any
}

// Cache is one set of cached results, keyed by K.
type Cache[K comparable] struct {
	ttl     time.Duration
	mu      sync.Mutex
	gen     uint64
	entries map[K]entry
}

// New is an empty cache whose results are reused for at most ttl.
func New[K comparable](ttl time.Duration) *Cache[K] {
	return &Cache[K]{ttl: ttl}
}

// Get is the cached result for key, if fresher than the cache's ttl.
func (c *Cache[K]) Get(key K) (any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.at) >= c.ttl {
		return nil, false
	}
	return e.result, true
}

// Begin stamps a computation of a result; pass the stamp to Put.
func (c *Cache[K]) Begin() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// Put caches result for key, unless an Invalidate ran since stamp. It reports
// whether the result was stored.
func (c *Cache[K]) Put(key K, stamp uint64, result any) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if stamp != c.gen {
		return false
	}
	if c.entries == nil {
		c.entries = map[K]entry{}
	}
	c.entries[key] = entry{at: time.Now(), result: result}
	return true
}

// Invalidate drops the cached results of keys, and every result being
// computed now. Callers invalidate after committing the change.
func (c *Cache[K]) Invalidate(keys ...K) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for _, k := range keys {
		delete(c.entries, k)
	}
}
