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
	idemMaxEntries = 10000 // bounded memory (audit MED-2)
)

// IdemStore runs a write exactly once per (connection, tool, key) and caches its
// SUCCESSFUL result, so a repeat — including a concurrent one — returns the
// first outcome without re-executing. Keys are namespaced by connection and
// tool, bound to a hash of the arguments, expire after idemTTL, and the store
// is size-bounded. A failed execution is not cached: the same key may retry.
type IdemStore struct {
	mu  sync.Mutex
	m   map[string]*idemEntry
	now func() time.Time
}

type idemEntry struct {
	mu       sync.Mutex // serialises executions of this key
	argsHash [32]byte
	created  time.Time
	done     bool
	result   any
}

// NewIdemStore builds an empty idempotency store.
func NewIdemStore() *IdemStore {
	return &IdemStore{m: make(map[string]*idemEntry), now: time.Now}
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
	if e != nil && now.Sub(e.created) > idemTTL {
		delete(s.m, k)
		e = nil
	}
	if e == nil {
		s.evictLocked(now)
		e = &idemEntry{argsHash: args, created: now}
		s.m[k] = e
	}
	s.mu.Unlock()

	if e.argsHash != args {
		return nil, ErrIdempotencyConflict
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return e.result, nil
	}
	res, err := fn()
	if err != nil {
		return nil, err // not cached: a transient failure may be retried with the same key
	}
	e.done, e.result = true, res
	return res, nil
}

// evictLocked drops expired entries and, if still at capacity, the oldest one.
func (s *IdemStore) evictLocked(now time.Time) {
	if len(s.m) < idemMaxEntries {
		return
	}
	var oldestK string
	var oldest time.Time
	for k, e := range s.m {
		if now.Sub(e.created) > idemTTL {
			delete(s.m, k)
			continue
		}
		if oldestK == "" || e.created.Before(oldest) {
			oldestK, oldest = k, e.created
		}
	}
	if len(s.m) >= idemMaxEntries && oldestK != "" {
		delete(s.m, oldestK)
	}
}

// size reports the number of entries (tests).
func (s *IdemStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}
