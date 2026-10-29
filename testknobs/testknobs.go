// Package testknobs holds the switches the regtest test hooks turn: injected
// faults and overridden cash limits (POST /api/test/faults and
// /api/test/cash/limits, testhooks builds only).
//
// Only a testhooks build can set them (set_testhooks.go). In every other build
// each switch keeps its zero value, and every read below answers the
// production behaviour: no fault, the configured limits.
package testknobs

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// gate is a switch callers can wait on until it is turned off.
type gate struct {
	mu  sync.Mutex
	on  bool
	off chan struct{} // closed when the gate turns off
}

func (g *gate) isOn() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.on
}

// wait blocks while the gate is on; ctx ends the wait early with its error.
func (g *gate) wait(ctx context.Context) error {
	g.mu.Lock()
	if !g.on {
		g.mu.Unlock()
		return nil
	}
	off := g.off
	g.mu.Unlock()
	select {
	case <-off:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var (
	stallCommit      gate
	holdPayments     gate
	breakSigner      atomic.Bool
	dropReply        atomic.Bool
	dropBeforeCommit atomic.Bool
	routeOverBound   atomic.Bool
)

// StallCommit reports whether commits are held (FaultStallCommit).
func StallCommit() bool { return stallCommit.isOn() }

// WaitCommit blocks while commits are held.
func WaitCommit(ctx context.Context) error { return stallCommit.wait(ctx) }

// HoldPayments reports whether outgoing payments are held (FaultHoldPayments).
func HoldPayments() bool { return holdPayments.isOn() }

// WaitPayments blocks while outgoing payments are held.
func WaitPayments(ctx context.Context) error { return holdPayments.wait(ctx) }

// SignerBroken reports whether the node signer is to fail (FaultBreakSigner).
func SignerBroken() bool { return breakSigner.Load() }

// DropReply reports whether an envelope is served without a reply
// (FaultDropReply).
func DropReply() bool { return dropReply.Load() }

// DropBeforeCommit reports whether an envelope is dropped unserved, its nonce
// released (FaultDropBeforeCommit).
func DropBeforeCommit() bool { return dropBeforeCommit.Load() }

// TakeRouteOverBound reports, once, that the next external payment's route
// costs more than its fee bound (FaultRouteOverBound); the switch then turns
// itself off.
func TakeRouteOverBound() bool { return routeOverBound.CompareAndSwap(true, false) }

// Limit overrides: unset (the zero value) means the configured limit applies.
var (
	limitsMu     sync.Mutex
	liveCapSet   bool
	liveCap      int
	spinOffSet   bool
	spinOffN     int
	spinOffPer   time.Duration
	spinOffCount = map[string]*window{}
)

type window struct {
	n       int
	resetAt time.Time
}

// LiveWalletCap is the overridden cap on a Cash Hub's live bills, when one is
// set; 0 lifts the cap.
func LiveWalletCap() (int, bool) {
	limitsMu.Lock()
	defer limitsMu.Unlock()
	return liveCap, liveCapSet
}

// SpinOffRateSet reports whether the lineage spin-off rate is overridden.
func SpinOffRateSet() bool {
	limitsMu.Lock()
	defer limitsMu.Unlock()
	return spinOffSet
}

// AllowSpinOff charges one spin-off to lineage under the overridden rate: n per
// window, n 0 lifting it. Call it only when SpinOffRateSet.
func AllowSpinOff(lineage string, now time.Time) bool {
	limitsMu.Lock()
	defer limitsMu.Unlock()
	if !spinOffSet || spinOffN <= 0 {
		return true
	}
	w := spinOffCount[lineage]
	if w == nil || !now.Before(w.resetAt) {
		spinOffCount[lineage] = &window{n: 1, resetAt: now.Add(spinOffPer)}
		return true
	}
	if w.n >= spinOffN {
		return false
	}
	w.n++
	return true
}
