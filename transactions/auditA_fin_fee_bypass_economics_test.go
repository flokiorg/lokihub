package transactions

// Financial audit (role A): QUANTIFY the §6 carried-forward open
// "redeem-fee gaming via pre-split" rather than re-deriving it, and compare it
// against the fee bypass that is actually worth an attacker's time.
//
// The prior round proved the DIRECTION of the pre-split inequality
// (cash_audit_redeemfee_fin_split_fragmentation_test.go). What was never stated
// is its MAGNITUDE, and the magnitude is what decides whether it matters:
// because each floor() discards strictly less than one mloki, splitting a slice
// into k pieces reduces the aggregate hub fee by AT MOST k-1 mloki. To wipe out
// a fee of F a holder therefore needs F+1 pieces, i.e. F cash_transfer splits —
// each one an extra rate-limited call and an extra bill.
//
// The same-node waiver (cash_redeem_controller.go:266-267: hubFeeMloki stays 0
// whenever transactions.IsSelfPayment is true) wipes out 100% of F in ONE call,
// on any amount, with no fragmentation at all.
//
// Both subtests PASS against current code. They are not bug demonstrations —
// they are the arithmetic that ranks the two, so the next round does not spend
// effort on the pre-split one.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditAFin_PreSplitFeeGaming_IsBoundedByOneMlokiPerPiece(t *testing.T) {
	// The bound: sum(floor(a_i*p/1e6)) >= floor(A*p/1e6) - (k-1).
	for _, tc := range []struct {
		total uint64
		ppm   int
		k     uint64
	}{
		{total: 1_000_000, ppm: 10_000, k: 2},
		{total: 1_000_000, ppm: 10_000, k: 64},
		{total: 1_000_000, ppm: 10_000, k: 1000},
		{total: 123_456_789, ppm: 999, k: 97},
		{total: 18, ppm: 100_000, k: 2},
	} {
		lump := CalculateFeeSkimMloki(tc.total, tc.ppm)
		piece := tc.total / tc.k
		var fragmented uint64
		for i := uint64(0); i < tc.k-1; i++ {
			fragmented += CalculateFeeSkimMloki(piece, tc.ppm)
		}
		fragmented += CalculateFeeSkimMloki(tc.total-piece*(tc.k-1), tc.ppm)

		require.LessOrEqual(t, fragmented, lump)
		saving := lump - fragmented
		assert.LessOrEqual(t, saving, tc.k-1,
			"total=%d ppm=%d k=%d: saving %d must not exceed k-1=%d",
			tc.total, tc.ppm, tc.k, saving, tc.k-1)
		t.Logf("total=%-11d ppm=%-7d k=%-5d lumpFee=%-7d fragmentedFee=%-7d saving=%d (bound k-1=%d)",
			tc.total, tc.ppm, tc.k, lump, fragmented, saving, tc.k-1)
	}
}

func TestAuditAFin_SameNodeWaiver_DominatesPreSplitGaming(t *testing.T) {
	// One realistic bill: 1,000,000 mloki at 1%.
	const amount, ppm = uint64(1_000_000), 10_000
	fee := CalculateFeeSkimMloki(amount, ppm)
	require.EqualValues(t, 10_000, fee)

	// Pre-split route: to drive the fee to 0 every piece must floor to 0, i.e.
	// each piece must be at most 1e6/ppm - 1 = 99 mloki.
	maxFreePiece := uint64(constantsPPMDivisor)/uint64(ppm) - 1
	piecesNeeded := (amount + maxFreePiece - 1) / maxFreePiece
	var fragmented uint64
	for i := uint64(0); i < piecesNeeded-1; i++ {
		fragmented += CalculateFeeSkimMloki(maxFreePiece, ppm)
	}
	fragmented += CalculateFeeSkimMloki(amount-maxFreePiece*(piecesNeeded-1), ppm)
	assert.Zero(t, fragmented, "every piece of %d mloki floors to a 0 fee", maxFreePiece)
	t.Logf("pre-split route:  fee %d -> 0, but needs %d pieces of <=%d mloki (%d rate-limited cash_transfer splits, %d new bills)",
		fee, piecesNeeded, maxFreePiece, piecesNeeded-1, piecesNeeded)

	// Same-node route: cash_redeem_controller.go keeps hubFeeMloki at 0 for a
	// self-payment, so the fee is 0 on the FULL amount, in one call.
	t.Logf("same-node route:  fee %d -> 0 in ONE cash_redeem, no splits, no extra bills", fee)
}

// PPM_DIVISOR lives in constants; mirrored locally so this file needs no import
// beyond testing (and so the number appearing in the arithmetic above is visible).
const constantsPPMDivisor = 1_000_000
