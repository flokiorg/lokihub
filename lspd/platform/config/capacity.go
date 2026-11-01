package config

// RequiredCapacityLoki computes the real on-chain capital a channel open
// commits, given the gross amount coming in and the net amount that must
// cross the channel after fees. Shared by the interceptor's openChannel
// and by both LSPS1/LSPS2 handlers' liquidity checks and reservation
// bookkeeping (Phase 7, F-24) -- before this existed, openChannel computed
// its own capacity inline and the LSPS1/2 handlers separately checked (and
// would have reserved) a smaller, wrong number, so the two could silently
// drift apart. All arguments are in loki, not mloki.
//
// Deliberately does NOT clamp to the physical/business floors
// (MinChannelCapacityLoki, JitMinChannelBalanceLoki): this codebase's real
// behavior at every existing call site is to REJECT a request whose
// capacity falls below a floor, never to silently open a bigger channel
// than what was requested/quoted (see capacity_matrix_test.go's
// "-> Reject" scenarios, and openChannel's own error return below the
// floor). Floor enforcement stays each caller's own explicit, separate
// check -- this function only computes the two terms that determine
// capacity when the request is otherwise valid:
//  1. incomingLoki + AdditionalChannelCapacity -- the base case.
//  2. The BOLT2 ~1% reserve top-up: a channel can never forward more than
//     ~99% of its own capacity (the funding party must keep that reserve on
//     its own side), so if outgoingLoki/0.99 plus padding would exceed the
//     base case, capacity must grow to cover it. This is the exact defect
//     root-caused 2026-07-16 in the interceptor's own openChannel (see its
//     comment) -- a payment could become permanently unroutable if capacity
//     didn't budget for this. Reserving less than this in the ledger would
//     reintroduce the same mistake one layer up, in accounting instead of
//     routing.
func RequiredCapacityLoki(incomingLoki, outgoingLoki uint64, cfg *NodeConfig, isJit bool) uint64 {
	padding := uint64(cfg.AdditionalChannelCapacity)
	capacity := incomingLoki + padding

	if minCapacity := outgoingLoki*100/99 + padding; minCapacity > capacity {
		capacity = minCapacity
	}

	return capacity
}

// MeetsCapacityFloors reports whether capacity clears both the physical
// network floor and (for JIT opens, when set) the business floor -- the
// shared predicate behind every "-> Reject" path in openChannel and the
// LSPS1/2 handlers' own validation, so all three call sites agree on
// exactly the same condition.
func MeetsCapacityFloors(capacity uint64, cfg *NodeConfig, isJit bool) bool {
	if capacity < cfg.MinChannelCapacityLoki {
		return false
	}
	if isJit && cfg.JitMinChannelBalanceLoki != nil && capacity < *cfg.JitMinChannelBalanceLoki {
		return false
	}
	return true
}
