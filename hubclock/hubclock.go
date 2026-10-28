// Package hubclock is the clock the Hub's protocol state runs on: slice and
// wallet expiry, order windows, lookups and their windows, records and
// tombstone retention, join freshness at commit, rotations, payouts and
// listings.
//
// It is the wall clock plus an offset that only the regtest test hooks can set
// (POST /api/test/clock {scope: "all"}, testhooks builds); in every other build
// the offset stays zero and Now is time.Now.
//
// What a client stamps is judged on the wall clock, never on this one: the
// envelope's not_after and created_at, item, bill and claim proofs, and a
// call's deadline at its commit (Wall). A client signs with its own wall clock,
// so moving the Hub forward must not make every later request look stale.
package hubclock

import (
	"sync"
	"sync/atomic"
	"time"
)

var (
	offsetSecs atomic.Int64

	movedMu sync.Mutex
	moved   = make(chan struct{})
)

// Moved is closed when the clock is next moved, so a periodic job can run at
// once rather than at its next tick. Read it afresh for every wait.
func Moved() <-chan struct{} {
	movedMu.Lock()
	defer movedMu.Unlock()
	return moved
}

// Now is the Hub's clock: the wall clock plus the test offset.
func Now() time.Time {
	if o := offsetSecs.Load(); o != 0 {
		return time.Now().Add(time.Duration(o) * time.Second)
	}
	return time.Now()
}

// Offset is how far, in seconds, the Hub's clock runs ahead of the wall clock.
func Offset() int64 { return offsetSecs.Load() }

// Wall maps an instant read from Now (or from a clock derived from it) back to
// the wall clock, for the checks a client's own timestamps are judged by.
func Wall(t time.Time) time.Time {
	if o := offsetSecs.Load(); o != 0 {
		return t.Add(-time.Duration(o) * time.Second)
	}
	return t
}
