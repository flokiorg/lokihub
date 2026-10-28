//go:build testhooks

package testknobs

import "time"

// Faults is the set of injected faults; a nil field is left as it is.
type Faults struct {
	StallCommit      *bool `json:"stall_commit,omitempty"`
	BreakSigner      *bool `json:"break_signer,omitempty"`
	DropReply        *bool `json:"drop_reply,omitempty"`
	DropBeforeCommit *bool `json:"drop_before_commit,omitempty"`
	RouteOverBound   *bool `json:"route_over_bound,omitempty"`
	HoldPayments     *bool `json:"hold_payments,omitempty"`
}

// SetFaults applies f after clearing every fault when reset is set, and
// returns the faults now on. Clearing stall_commit or hold_payments releases
// whatever waits on it.
func SetFaults(f Faults, reset bool) map[string]bool {
	if reset {
		stallCommit.set(false)
		holdPayments.set(false)
		breakSigner.Store(false)
		dropReply.Store(false)
		dropBeforeCommit.Store(false)
		routeOverBound.Store(false)
	}
	if f.StallCommit != nil {
		stallCommit.set(*f.StallCommit)
	}
	if f.HoldPayments != nil {
		holdPayments.set(*f.HoldPayments)
	}
	for _, s := range []struct {
		v  *bool
		to interface{ Store(bool) }
	}{{f.BreakSigner, &breakSigner}, {f.DropReply, &dropReply}, {f.DropBeforeCommit, &dropBeforeCommit}, {f.RouteOverBound, &routeOverBound}} {
		if s.v != nil {
			s.to.Store(*s.v)
		}
	}
	return map[string]bool{
		"stall_commit": stallCommit.isOn(), "hold_payments": holdPayments.isOn(),
		"break_signer": breakSigner.Load(), "drop_reply": dropReply.Load(),
		"drop_before_commit": dropBeforeCommit.Load(), "route_over_bound": routeOverBound.Load(),
	}
}

// Limits overrides the cash limits; a nil field is left as it is.
type Limits struct {
	// LiveWalletCap caps a Cash Hub's live bills; 0 lifts the cap.
	LiveWalletCap *int `json:"live_wallet_cap,omitempty"`
	// SpinOffRate bounds a bill lineage's spin-offs per PerSecs; 0 lifts it.
	SpinOffRate *int   `json:"spin_off_rate,omitempty"`
	PerSecs     *int64 `json:"per_secs,omitempty"`
}

// SetLimits applies l after dropping every override when reset is set, and
// returns the overrides now in force.
func SetLimits(l Limits, reset bool) map[string]any {
	limitsMu.Lock()
	defer limitsMu.Unlock()
	if reset {
		liveCapSet, liveCap = false, 0
		spinOffSet, spinOffN, spinOffPer = false, 0, 0
		spinOffCount = map[string]*window{}
	}
	if l.LiveWalletCap != nil {
		liveCapSet, liveCap = true, max(*l.LiveWalletCap, 0)
	}
	if l.SpinOffRate != nil || l.PerSecs != nil {
		if l.SpinOffRate != nil {
			spinOffN = max(*l.SpinOffRate, 0)
		}
		if l.PerSecs != nil && *l.PerSecs > 0 {
			spinOffPer = time.Duration(*l.PerSecs) * time.Second
		}
		if spinOffPer == 0 {
			spinOffPer = time.Hour
		}
		spinOffSet = true
		spinOffCount = map[string]*window{}
	}
	out := map[string]any{}
	if liveCapSet {
		out["live_wallet_cap"] = liveCap
	}
	if spinOffSet {
		out["spin_off_rate"] = spinOffN
		out["per_secs"] = int64(spinOffPer / time.Second)
	}
	return out
}

// set turns the gate on or off; turning it off releases its waiters.
func (g *gate) set(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case on && !g.on:
		g.off = make(chan struct{})
	case !on && g.on:
		close(g.off)
	}
	g.on = on
}
