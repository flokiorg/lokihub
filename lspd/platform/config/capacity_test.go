package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequiredCapacityLoki_BaseCase(t *testing.T) {
	cfg := &NodeConfig{AdditionalChannelCapacity: 10_000, MinChannelCapacityLoki: 20_000}
	// incoming + padding = 100_000 + 10_000 = 110_000; the outgoing/reserve
	// term (90_000*100/99 + 10_000 ~= 100_909) is smaller and doesn't bind.
	got := RequiredCapacityLoki(100_000, 90_000, cfg, false)
	assert.Equal(t, uint64(110_000), got)
}

func TestRequiredCapacityLoki_Bolt2ReserveTopUpBinds(t *testing.T) {
	// Small fee rate (net ~= gross) makes the 1%-reserve term the largest:
	// outgoing*100/99 > incoming when outgoing is close to incoming.
	cfg := &NodeConfig{AdditionalChannelCapacity: 0, MinChannelCapacityLoki: 20_000}
	incoming := uint64(1_000_000)
	outgoing := uint64(999_000) // fee is only 1000 loki, ~0.1%
	got := RequiredCapacityLoki(incoming, outgoing, cfg, false)
	want := outgoing * 100 / 99 // 1,009,090
	assert.Greater(t, want, incoming, "test setup should actually exercise the reserve term")
	assert.Equal(t, want, got)
}

func TestRequiredCapacityLoki_DoesNotClampToFloors(t *testing.T) {
	// Deliberate: this codebase's real behavior is reject-below-floor, not
	// silently-open-bigger-than-requested (capacity_matrix_test.go's
	// "-> Reject" scenarios). RequiredCapacityLoki must return the raw
	// computed figure even when it's below a configured floor -- floor
	// enforcement is MeetsCapacityFloors' job, not this function's.
	cfg := &NodeConfig{AdditionalChannelCapacity: 0, MinChannelCapacityLoki: 20_000}
	got := RequiredCapacityLoki(100, 90, cfg, false)
	assert.Equal(t, uint64(100), got, "must return the raw figure, not clamp up to the floor")
}

func TestRequiredCapacityLoki_ZeroAmounts(t *testing.T) {
	cfg := &NodeConfig{AdditionalChannelCapacity: 0, MinChannelCapacityLoki: 20_000}
	got := RequiredCapacityLoki(0, 0, cfg, false)
	assert.Equal(t, uint64(0), got)
}

func TestRequiredCapacityLoki_MonotonicInIncoming(t *testing.T) {
	cfg := &NodeConfig{AdditionalChannelCapacity: 1_000, MinChannelCapacityLoki: 20_000}
	small := RequiredCapacityLoki(100_000, 90_000, cfg, false)
	large := RequiredCapacityLoki(200_000, 180_000, cfg, false)
	assert.Greater(t, large, small, "a bigger payment must never require less capacity than a smaller one")
}

func TestMeetsCapacityFloors_PhysicalFloor(t *testing.T) {
	cfg := &NodeConfig{MinChannelCapacityLoki: 20_000}
	assert.False(t, MeetsCapacityFloors(19_999, cfg, false))
	assert.True(t, MeetsCapacityFloors(20_000, cfg, false))
	assert.True(t, MeetsCapacityFloors(20_001, cfg, false))
}

func TestMeetsCapacityFloors_JitBusinessFloorOnlyForJit(t *testing.T) {
	floor := uint64(50_000)
	cfg := &NodeConfig{MinChannelCapacityLoki: 20_000, JitMinChannelBalanceLoki: &floor}

	assert.False(t, MeetsCapacityFloors(30_000, cfg, true), "below the JIT business floor must fail for a JIT open")
	assert.True(t, MeetsCapacityFloors(30_000, cfg, false), "the JIT-only business floor must not apply to a non-JIT open")
	assert.True(t, MeetsCapacityFloors(50_000, cfg, true))
}

func TestMeetsCapacityFloors_NilJitFloorIsSafe(t *testing.T) {
	cfg := &NodeConfig{MinChannelCapacityLoki: 20_000, JitMinChannelBalanceLoki: nil}
	assert.True(t, MeetsCapacityFloors(20_000, cfg, true), "nil JIT floor must not panic and must fall back to only the physical floor")
}
