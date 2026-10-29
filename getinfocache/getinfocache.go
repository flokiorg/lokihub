// Package getinfocache holds the get_info results lokihub may answer public
// connections from (NIP-CASH §Hub Rules MAY, plan OQ-47): an Order
// connection's, at most TTL old, dropped at once when its terms or retirement
// change. Entries are keyed by the connection's client key.
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

var cache sync.Map // client key -> entry

// Get is the cached result for key, if fresher than TTL.
func Get(key string) (any, bool) {
	v, ok := cache.Load(key)
	if !ok {
		return nil, false
	}
	e := v.(entry)
	if time.Since(e.at) >= TTL {
		return nil, false
	}
	return e.result, true
}

// Put caches result for key.
func Put(key string, result any) { cache.Store(key, entry{at: time.Now(), result: result}) }

// Invalidate drops the cached results of keys.
func Invalidate(keys ...string) {
	for _, k := range keys {
		cache.Delete(k)
	}
}
