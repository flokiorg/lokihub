package lsps

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeOpeningFee_ProportionalAboveFloor(t *testing.T) {
	fee, err := ComputeOpeningFee(10_000_000, 100, 10_000) // 1% of 10M mloki
	require.NoError(t, err)
	assert.EqualValues(t, 100_000, fee)
}

func TestComputeOpeningFee_FloorWinsWhenProportionalIsSmaller(t *testing.T) {
	fee, err := ComputeOpeningFee(1000, 500, 100) // proportional fee is tiny; floor dominates
	require.NoError(t, err)
	assert.EqualValues(t, 500, fee)
}

func TestComputeOpeningFee_RoundsUpToNearestMloki(t *testing.T) {
	// 1 mloki * 1 ppm = 0.000001 mloki proportional fee, which must round
	// up to 1 mloki (not truncate to 0) before the floor comparison.
	fee, err := ComputeOpeningFee(1, 0, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 1, fee)
}

func TestComputeOpeningFee_ZeroPaymentSize(t *testing.T) {
	fee, err := ComputeOpeningFee(0, 250, 10_000)
	require.NoError(t, err)
	assert.EqualValues(t, 250, fee, "zero payment size must still respect the floor, not divide by zero")
}

func TestComputeOpeningFee_ZeroProportionalUsesFloor(t *testing.T) {
	fee, err := ComputeOpeningFee(1_000_000, 750, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 750, fee)
}

func TestComputeOpeningFee_MultiplicationOverflowErrors(t *testing.T) {
	_, err := ComputeOpeningFee(math.MaxUint64, 0, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overflow")
}

func TestComputeOpeningFee_AdditionOverflowErrors(t *testing.T) {
	// A product close enough to MaxUint64 that adding the 999999 rounding
	// constant itself overflows, independent of the multiplication check.
	_, err := ComputeOpeningFee(math.MaxUint64, 0, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overflow")
}

// FuzzComputeOpeningFee is the fuzz coverage F-5's own acceptance
// criteria called for ("one fee function, fuzz-tested for overflow and
// rounding") but was never actually added when the function was first
// introduced. Asserts the properties that must hold for any input that
// doesn't overflow: the result is never below the floor, never below the
// exact (unrounded) proportional fee, and — when the floor isn't what
// determined the result — never more than one mloki above it, i.e.
// rounding is always up, by less than a full mloki.
func FuzzComputeOpeningFee(f *testing.F) {
	f.Add(uint64(0), uint64(0), uint64(0))
	f.Add(uint64(1), uint64(0), uint64(1))
	f.Add(uint64(10_000_000), uint64(1000), uint64(10_000))
	f.Add(uint64(29_995_000), uint64(100), uint64(100))
	f.Add(uint64(math.MaxUint64), uint64(0), uint64(1))
	f.Add(uint64(0), uint64(math.MaxUint64), uint64(0))

	f.Fuzz(func(t *testing.T, paymentSizeMloki, minFeeMloki, proportional uint64) {
		fee, err := ComputeOpeningFee(paymentSizeMloki, minFeeMloki, proportional)
		if err != nil {
			// Only overflow is a legitimate error; anything else is a bug.
			require.Contains(t, err.Error(), "overflow")
			return
		}

		require.GreaterOrEqual(t, fee, minFeeMloki, "result must never fall below the floor")

		if paymentSizeMloki != 0 && proportional != 0 {
			// prod doesn't overflow here since ComputeOpeningFee didn't error.
			prod := paymentSizeMloki * proportional
			exactProportional := prod / 1_000_000
			require.GreaterOrEqual(t, fee, exactProportional, "result must never round down below the true proportional fee")
			if fee > minFeeMloki {
				// The floor didn't determine the result, so it must be the
				// ceiling-rounded proportional fee, which overshoots the
				// exact value by less than one full mloki. Compares via
				// subtraction (fee >= exactProportional already
				// established above), not addition, since minFeeMloki can
				// be arbitrarily large and exactProportional+N could
				// itself overflow.
				require.Less(t, fee-exactProportional, uint64(2))
			}
		}
	})
}
