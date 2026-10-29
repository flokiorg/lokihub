// Package getinfocache holds the get_info results lokihub may answer public
// connections from (NIP-CASH §Hub Rules MAY, plan OQ-47): an Order
// connection's, keyed by its client key, and a listed circle connection's,
// keyed by its app id; each at most TTL old, dropped at once when what it
// shows changes.
package getinfocache

import (
	"sync"
	"time"
)

// TTL is the most a cached result is reused.
const TTL = 5 * time.Second

type entry struct {
	at     time.Time
	result any
}

// Cache is one set of cached results. A result computed before an
// invalidation is never stored after it: Begin stamps the computation, and Put
// drops a result whose stamp an Invalidate has passed since.
type Cache[K comparable] struct {
	mu      sync.Mutex
	gen     uint64
	entries map[K]entry
}

// Get is the cached result for key, if fresher than TTL.
func (c *Cache[K]) Get(key K) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.at) >= TTL {
		return nil, false
	}
	return e.result, true
}

// Begin stamps a computation of a result; pass the stamp to Put.
func (c *Cache[K]) Begin() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// Put caches result for key, unless an Invalidate ran since stamp.
func (c *Cache[K]) Put(key K, stamp uint64, result any) bool {
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
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for _, k := range keys {
		delete(c.entries, k)
	}
}

// Orders is the cache of Order connections' get_info, by client key.
var Orders = &Cache[string]{}

// Listed is the cache of listed circle connections' get_info, by app id.
var Listed = &Cache[uint]{}
