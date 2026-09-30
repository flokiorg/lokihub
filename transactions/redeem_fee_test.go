package transactions

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// The redeem fee is what the Hub WITHHOLDS from a slice to pay for delivering it,
// and it is also the cap handed to LND as FeeLimitMsat. Those two uses are why the
// arithmetic matters beyond rounding: payout = amount - fee, cap = fee, so
// payout + cap == amount exactly. If the fee ever exceeded the amount, the payout
// would underflow; if it were computed differently at the quote and the cap, the
// Hub would promise one price and spend another.
func TestCalculateRedeemFeeMloki(t *testing.T) {
	tests := []struct {
		name   string
		amount uint64
		base   int64
		ppm    int
		want   uint64
		why    string
	}{
		{
			name: "NoFeeConfigured", amount: 1000, base: 0, ppm: 0, want: 0,
			why: "a Hub charging nothing withholds nothing — the old default, unchanged",
		},
		{
			name: "PpmOnly", amount: 1_000_000, base: 0, ppm: 10_000, want: 10_000,
			why: "1% of 1,000,000 — the pre-existing proportional behaviour must be untouched",
		},
		{
			name: "BaseOnly", amount: 1000, base: 500, ppm: 0, want: 500,
			why: "a flat fee is charged whatever the amount; this is the whole point on small slices",
		},
		{
			name: "BasePlusPpm", amount: 1_000_000, base: 500, ppm: 10_000, want: 10_500,
			why: "the two components add — the base does not replace the proportional part",
		},
		{
			name: "SmallSliceIsDominatedByTheBase", amount: 1000, base: 500, ppm: 1000, want: 501,
			why: "1,000 at 0.1% earns 1 while a real route costs hundreds; the base is what closes that gap",
		},
		{
			name: "FeeIsCappedAtTheAmount", amount: 300, base: 500, ppm: 0, want: 300,
			why: "a fee may never exceed what it is charged on, or payout = amount - fee underflows",
		},
		{
			name: "BaseOverflowSaturates", amount: 1000, base: math.MaxInt64, ppm: 1_000_000, want: 1000,
			why: "saturation, not wraparound — a wrapped fee would quote a payout larger than the slice",
		},
		{
			name: "NegativeBaseIsIgnored", amount: 1000, base: -500, ppm: 0, want: 0,
			why: "a defensively-negative config must never become a rebate that pays the recipient extra",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateRedeemFeeMloki(tt.amount, tt.base, tt.ppm)
			require.Equal(t, tt.want, got, tt.why)
			require.LessOrEqual(t, got, tt.amount,
				"the fee must never exceed the amount, or the payout underflows")
		})
	}
}

// TestCalculateRedeemFeeMloki_PayoutPlusCapNeverExceedsTheSlice is the invariant the
// whole change exists to establish, stated directly: whatever the Hub pays out plus
// whatever it permits the route to cost must fit inside the slice, so the Hub's own
// balance is never touched by a redemption.
func TestCalculateRedeemFeeMloki_PayoutPlusCapNeverExceedsTheSlice(t *testing.T) {
	for _, amount := range []uint64{1, 300, 1000, 50_000, 1_000_000, math.MaxInt64} {
		for _, base := range []int64{0, 1, 500, 10_000} {
			for _, ppm := range []int{0, 1000, 10_000, 1_000_000} {
				fee := CalculateRedeemFeeMloki(amount, base, ppm)
				require.LessOrEqual(t, fee, amount)
				payout := amount - fee // the exact expression cash_redeem uses
				require.Equal(t, amount, payout+fee,
					"payout(%d) + cap(%d) must equal the slice(%d) for base=%d ppm=%d",
					payout, fee, amount, base, ppm)
			}
		}
	}
}

// TestCalculateRedeemFeeMloki_IsNotTheCircleSkim pins the separation. CalculateFeeSkimMloki
// computes a circle_hub's forwarding cut and is deliberately still purely proportional;
// folding a base into it would silently re-price every circle payment.
func TestCalculateRedeemFeeMloki_IsNotTheCircleSkim(t *testing.T) {
	const amount, ppm = 1_000_000, 10_000
	require.Equal(t, uint64(10_000), CalculateFeeSkimMloki(amount, ppm),
		"the circle skim must stay proportional-only")
	require.Equal(t, uint64(10_500), CalculateRedeemFeeMloki(amount, 500, ppm),
		"the redeem fee adds its base on top")
	require.Equal(t, CalculateFeeSkimMloki(amount, ppm), CalculateRedeemFeeMloki(amount, 0, ppm),
		"with no base the two must agree exactly, so configuring nothing changes nothing")
}
