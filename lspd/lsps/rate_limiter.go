package lsps

import (
	"sync"
	"time"
)

// defaultBurstLimit/defaultBurstWindow: how many mutating LSPS1/2/5 calls
// (create_order, buy, set_webhook) one peer may make per window before the burst
// shaper starts rejecting (Phase 7, F-17/T7-7). Deliberately generous --
// this layer exists to blunt a tight-loop spam script, not to be a
// legitimate client's normal rate limit; the DB-backed standing cap and
// fleet ceiling are the real business-rule limits.
const (
	defaultBurstLimit  = 20
	defaultBurstWindow = 10 * time.Second
	// The read limiter is looser than the mutating burst limiter: reads are the
	// hot per-purchase path (lsps2.get_info), so the cap only has to stop a
	// flood, not shape normal traffic.
	defaultReadLimit  = 60
	defaultReadWindow = 10 * time.Second
)

// peerBurstLimiter is a simple fixed-window per-peer request counter: O(1)
// per check, no I/O, no per-request allocation once a peer's window
// exists. Not a true token bucket, but equivalent for burst-shaping
// purposes and simpler to reason about. Safe for concurrent use.
//
// Deliberately one global mutex, not sharded/striped by peer: this only
// guards lsps1.create_order/lsps2.buy -- channel-open negotiation calls,
// not the HTLC-forwarding hot path -- so realistic call volume is at most
// tens to low hundreds per second even under a deliberate Sybil attempt,
// against an O(1) critical section (map lookup + counter compare/
// increment). EvictExpired's O(n) full-map scan is the longer hold, but
// it only runs once per BurstLimiterEvictionInterval (cmd/flspd/run.go,
// currently 5 minutes), not per-request. Sharding would add real
// complexity (picking a shard count, sweeping every shard on eviction)
// for contention that doesn't materialize at this call frequency;
// revisit only if profiling ever shows otherwise.
type peerBurstLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	seen   map[string]*burstWindow
}

type burstWindow struct {
	count  int
	endsAt time.Time
}

func newPeerBurstLimiter(limit int, window time.Duration) *peerBurstLimiter {
	return &peerBurstLimiter{
		limit:  limit,
		window: window,
		seen:   make(map[string]*burstWindow),
	}
}

// Allow reports whether peerId may proceed right now, incrementing its
// count if so. Must be the first check in any handler it guards --
// checked before promise verification, DB reads, or any other real work,
// so a spam burst is rejected as cheaply as possible. Nil-safe by
// convention (like OrderWatcher): a *Handlers built via a bare struct
// literal instead of NewHandlers (common in tests that only exercise one
// method) has a nil burstLimiter, and should behave as "no burst shaping
// configured," not panic.
func (l *peerBurstLimiter) Allow(peerId string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	w, ok := l.seen[peerId]
	if !ok || now.After(w.endsAt) {
		l.seen[peerId] = &burstWindow{count: 1, endsAt: now.Add(l.window)}
		return true
	}
	if w.count >= l.limit {
		return false
	}
	w.count++
	return true
}

// EvictExpired bounds memory growth under a real Sybil attempt (many
// fresh peer identities, each getting its own bucket that would otherwise
// live forever) -- intended to be called periodically (e.g. every few
// minutes), not on every request. Nil-safe (see Allow's doc comment): a
// no-op on a nil receiver, not a panic.
func (l *peerBurstLimiter) EvictExpired() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for k, w := range l.seen {
		if now.After(w.endsAt) {
			delete(l.seen, k)
		}
	}
}

// StartEvictionLoop runs EvictExpired on interval until stopCh is closed.
// Intended to be launched once, e.g. `go handlers.BurstLimiter().StartEvictionLoop(stopCh, 5*time.Minute)`.
// Nil-safe (see Allow's doc comment): still honors stopCh and returns
// cleanly on a nil receiver, rather than panicking on its first tick.
func (l *peerBurstLimiter) StartEvictionLoop(stopCh <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.EvictExpired()
		case <-stopCh:
			return
		}
	}
}
