package mcp

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrMissingIdempotencyKey is returned when a write tool is called without an
// idempotency key. Writes require one so a retried or concurrently-duplicated
// call cannot double-execute (audit M-REPLAY-1): MCP tools/call carries no
// request-id dedup of its own.
var ErrMissingIdempotencyKey = errors.New("mcp: write requires an idempotency_key")

// ErrIdempotencyConflict is returned when a key is reused with different
// arguments: replaying the first result would tell the model something it did
// not do ("paid B" when it paid A), so the call is refused instead.
var ErrIdempotencyConflict = errors.New("mcp: idempotency_key was already used with different arguments")

const (
	idemTTL        = 24 * time.Hour
	idemMaxEntries = 10000 // global bound on memory (audit MED-2)
	idemMaxPerApp  = 1000  // per connection: one assistant can never evict another's records (re-audit N3)
)

// IdemStore runs a write exactly once per (connection, tool, key) and caches its
// SUCCESSFUL result, so a repeat — including a concurrent one — returns the
// first outcome without re-executing. Keys are namespaced by connection and
// tool, bound to a hash of the arguments, expire after idemTTL, and the store
// is size-bounded. A failed execution is not cached: the same key may retry.
type IdemStore struct {
	mu    sync.Mutex
	m     map[string]*idemEntry
	count map[uint]int // entries per connection (kept in step with m)
	now   func() time.Time
}

type idemEntry struct {
	mu       sync.Mutex // serialises executions of this key
	appID    uint
	inflight int // callers currently inside Do for this key (guarded by the store mutex)
	argsHash [32]byte
	created  time.Time
	done     bool
	result   any
}

// NewIdemStore builds an empty idempotency store.
func NewIdemStore() *IdemStore {
	return &IdemStore{m: make(map[string]*idemEntry), count: map[uint]int{}, now: time.Now}
}

// ArgsHash hashes a tool's typed arguments for idempotency binding.
func ArgsHash(v any) [32]byte {
	b, _ := json.Marshal(v)
	return sha256.Sum256(b)
}

// Do executes fn at most once successfully for (appID, tool, key). Concurrent
// callers with the same key wait for the in-flight execution and receive its
// result; a key reused with different arguments gets ErrIdempotencyConflict.
func (s *IdemStore) Do(appID uint, tool, key string, args [32]byte, fn func() (any, error)) (any, error) {
	k := fmt.Sprintf("%d\x00%s\x00%s", appID, tool, key)
	s.mu.Lock()
	now := s.now()
	e := s.m[k]
	if e != nil && e.inflight == 0 && now.Sub(e.created) > idemTTL {
		s.delLocked(k)
		e = nil
	}
	if e == nil {
		if err := s.evictLocked(now, appID); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		e = &idemEntry{appID: appID, argsHash: args, created: now}
		s.m[k] = e
		s.count[appID]++
	}
	if e.argsHash != args {
		s.mu.Unlock()
		return nil, ErrIdempotencyConflict
	}
	e.inflight++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		e.inflight--
		s.mu.Unlock()
	}()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return e.result, nil
	}
	res, err := fn()
	if err != nil {
		// Not cached, and the key is released if nobody else is using it, so a
		// failed attempt never pins the key to its arguments (pentest F F-6).
		s.mu.Lock()
		if s.m[k] == e && !e.done && e.inflight <= 1 {
			s.delLocked(k)
		}
		s.mu.Unlock()
		return nil, err
	}
	e.done, e.result = true, res
	return res, nil
}

// ErrIdempotencyBusy — the connection has too many writes in flight to make
// room for a new idempotency record.
var ErrIdempotencyBusy = errors.New("mcp: too many writes in progress for this connection; retry shortly")

// evictLocked makes room for a new record of appID: expired entries go first;
// then, if the connection is at its own cap, its oldest settled record; then, if
// the store is at the global cap, the oldest settled record of the connection
// holding the most. Entries with a call in flight are never evicted (evicting
// one would let a concurrent retry execute the write again).
func (s *IdemStore) evictLocked(now time.Time, appID uint) error {
	if s.count[appID] < idemMaxPerApp && len(s.m) < idemMaxEntries {
		return nil // common case: no scan
	}
	for k, e := range s.m {
		if e.inflight == 0 && now.Sub(e.created) > idemTTL {
			s.delLocked(k)
		}
	}
	oldestOf := func(app uint) string {
		var key string
		var t time.Time
		for k, e := range s.m {
			if e.appID != app || e.inflight > 0 {
				continue
			}
			if key == "" || e.created.Before(t) {
				key, t = k, e.created
			}
		}
		return key
	}
	if s.count[appID] >= idemMaxPerApp {
		k := oldestOf(appID)
		if k == "" {
			return ErrIdempotencyBusy
		}
		s.delLocked(k)
	}
	if len(s.m) >= idemMaxEntries {
		var big uint
		for a, n := range s.count {
			if n > s.count[big] {
				big = a
			}
		}
		k := oldestOf(big)
		if k == "" {
			return ErrIdempotencyBusy
		}
		s.delLocked(k)
	}
	return nil
}

func (s *IdemStore) delLocked(k string) {
	if e, ok := s.m[k]; ok {
		delete(s.m, k)
		if s.count[e.appID]--; s.count[e.appID] <= 0 {
			delete(s.count, e.appID)
		}
	}
}

// size reports the number of entries (tests).
func (s *IdemStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}
