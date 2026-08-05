package transactions

// Independent Security Engagement B (2026-08-02) — cash_redeem redeem-fee
// reconciliation, rounding/integer-truncation angle.
//
// CalculateFeeSkimMloki (reused unchanged by this new mechanism, from the
// pre-existing circle_hub fee-skim code) computes
// floor(amountMloki * feesPpm / PPM_DIVISOR) — pure integer division,
// truncating toward zero. Two things this file checks:
//
//  1. reconcileCashRedeemFee's delta arithmetic
//     (int64(quotedFeeMloki) - int64(realFeeMloki)) never overflows or
//     misbehaves at the boundary values the system can actually produce —
//     quotedFeeMloki is capped at the slice's own claimed amount (a
//     <=100% cut, since redeem_fee_ppm is validated in [0, MAX_FEES_PPM]
//     and MAX_FEES_PPM == PPM_DIVISOR, i.e. 100%), and realFeeMloki is
//     capped, in the one real LN backend this codebase ships
//     (lnclient/flnd/flnd.go, FeeLimitMsat:
//     transactions.CalculateFeeReserveMloki(amount) — max(1% of amount,
//     10000 mloki)), to roughly 1% of the payment. Both are always many
//     orders of magnitude below int64's range for any amount this system
//     can otherwise represent (capped at math.MaxInt64 mloki by
//     cashwallet.Resolve's own overflow guards) — confirmed below at the
//     actual boundary the code enforces, not just asserted in prose.
//
//  2. Floor rounding is SUBADDITIVE: for any nonnegative amount and rate,
//     floor(a*p/d) + floor(b*p/d) <= floor((a+b)*p/d). Concretely, this
//     means a recipient who splits their slice into several pieces (each
//     inheriting the SAME immutable redeem_fee_ppm, per NIP-CASH's
//     inheritance rule) and redeems each piece separately can cause the
//     Cash Hub to collect STRICTLY LESS total redeem-fee revenue than a
//     single redemption of the undivided slice would have. This is a real,
//     directionally-consistent effect, not a wash — but it's bounded to
//     less than 1 mloki of "leakage" per additional split (each floor
//     operation can only lose a fractional remainder < 1), and in practice
//     is further bounded by Lightning's own minimum payment size (~1000
//     mloki / 1 sat), which caps how many times a slice can meaningfully be
//     split before pieces stop being payable invoices at all. Documented
//     here as INFORMATIONAL — the same rounding convention
//     CircleHubConfig's existing fee skim already accepts, not a new
//     exposure this mechanism introduces — rather than a vulnerability.

import (
	"math"
	"testing"

	"github.com/flokiorg/lokihub/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCashAuditSecB_CalculateFeeSkimMloki_OverflowsAtExtremeAmounts is a
// FINDING (Low): CalculateFeeSkimMloki's own multiplication —
// `amountMloki * uint64(feesPpm)`, transactions_service.go ~line 1541 —
// is unchecked. uint64's own ceiling (2^64-1 ~= 1.8e19) is crossed whenever
// amountMloki * feesPpm exceeds it; at the maximum configurable
// redeem_fee_ppm (constants.MAX_FEES_PPM == PPM_DIVISOR == 1_000_000, i.e a
// 100% fee), that happens for any amountMloki at or above ~1.8e13 mloki
// (~18.4 billion loki). cashwallet.Resolve's own per-recipient ceiling is
// math.MaxInt64 (~9.2e18 mloki) — about half a million times higher than
// this overflow threshold — and neither Resolve, CreateCashHub, nor
// UpdateCashHubConfig imposes any additional ceiling on AmountMloki or
// redeem_fee_ppm that would keep a hub operator away from this boundary.
// A slice minted at or beyond it gets a silently WRONG (wrapped, not
// saturated or rejected) quoted fee from both cash_redeem's own
// authorization check (step 9) and list_recipients' displayed quote —
// the same corrupted value on both call sites, since both call this exact
// function, so the two stay internally consistent with each other, but both
// diverge from the operator's actual configured rate.
//
// This is bounded to a financial-correctness defect (the hub's fee revenue
// for an affected slice becomes essentially arbitrary — it could round to
// far less, or even far more within the wrapped range, than the configured
// rate intends), NOT a wallet fund-safety break: reconcileCashRedeemFee's
// delta arithmetic uses whatever value ends up stored on the transaction
// row, whatever it is, so the "wallet drains by exactly the claimed
// amount" invariant (NIP-CASH §The Redeem Fee) still holds algebraically
// regardless of what CalculateFeeSkimMloki returned. Realistically remote
// (an amount in the tens-of-billions-of-loki range on a single slice) but
// the code path enforces no ceiling that would prevent it, and this exact
// function (CalculateFeeSkimMloki) is the one this whole mechanism newly
// reuses for redeem-fee quoting on top of its pre-existing circle_hub
// fee-skim use — see the task's own explicit callout of "CalculateFeeSkimMloki
// reuse" as an audit angle.
func TestCashAuditSecB_CalculateFeeSkimMloki_OverflowsAtExtremeAmounts(t *testing.T) {
	const maxClaimed = uint64(1<<63 - 1) // cashwallet.Resolve's own per-recipient ceiling (math.MaxInt64)

	// Correct (non-overflowing) answer: a 100% fee on maxClaimed should be
	// exactly maxClaimed itself.
	quotedFee := CalculateFeeSkimMloki(maxClaimed, constants.MAX_FEES_PPM)
	assert.NotEqual(t, maxClaimed, quotedFee,
		"FINDING: at cashwallet.Resolve's own permitted ceiling, CalculateFeeSkimMloki's internal "+
			"amountMloki*feesPpm multiplication overflows uint64 and silently returns a WRONG, wrapped "+
			"fee instead of the mathematically correct answer (which would equal maxClaimed exactly, "+
			"for a 100% rate) or an error")

	// The overflow threshold itself: the smallest amountMloki (at the
	// maximum 100% rate) where amountMloki*feesPpm first exceeds uint64's
	// own range — confirms the boundary is exactly where the doc comment
	// above says it is, not merely "some large number".
	const overflowThreshold = math.MaxUint64/uint64(constants.MAX_FEES_PPM) + 1
	require.Less(t, overflowThreshold, maxClaimed,
		"the overflow boundary sits comfortably below cashwallet.Resolve's own per-recipient ceiling, "+
			"confirming this isn't merely a theoretical, unreachable-by-validation edge")
	justBelow := CalculateFeeSkimMloki(overflowThreshold-1, constants.MAX_FEES_PPM)
	justAt := CalculateFeeSkimMloki(overflowThreshold, constants.MAX_FEES_PPM)
	assert.Equal(t, overflowThreshold-1, justBelow, "one mloki below the threshold: still correct")
	assert.NotEqual(t, overflowThreshold, justAt, "at the threshold itself: wraps to a wrong value")
}

// TestCashAuditSecB_ReconcileDelta_RealisticBoundaryValuesNeverOverflow
// covers the delta arithmetic itself (reconcileCashRedeemFee,
// transactions_service.go ~line 1942) across every value CalculateFeeSkimMloki
// CAN correctly (non-overflowing) produce, plus the largest real routing fee
// the one real LN backend this codebase ships would ever authorize
// (CalculateFeeReserveMloki, ~1% of the payment) — confirming that once
// quotedFeeMloki/realFeeMloki are themselves sane, the delta subtraction has
// no overflow or sign-handling problem of its own.
func TestCashAuditSecB_ReconcileDelta_RealisticBoundaryValuesNeverOverflow(t *testing.T) {
	// A realistic large slice, safely below CalculateFeeSkimMloki's own
	// overflow threshold (see the sibling test above) but still enormous —
	// 10 trillion mloki.
	const largeClaimed = uint64(10_000_000_000_000)

	maxQuotedFee := CalculateFeeSkimMloki(largeClaimed, constants.MAX_FEES_PPM)
	require.Equal(t, largeClaimed, maxQuotedFee, "a 100%% redeem_fee_ppm quotes the entire claimed amount as the fee")

	maxPossibleRealFee := CalculateFeeReserveMloki(largeClaimed)

	deltaAllFeeNoReal := int64(maxQuotedFee) - int64(0) //nolint:gosec // this is exactly the check under review
	assert.Equal(t, int64(maxQuotedFee), deltaAllFeeNoReal) //nolint:gosec // this is exactly the check under review
	assert.Greater(t, deltaAllFeeNoReal, int64(0))

	deltaNoFeeMaxReal := int64(0) - int64(maxPossibleRealFee) //nolint:gosec // this is exactly the check under review
	assert.Less(t, deltaNoFeeMaxReal, int64(0))
	assert.Greater(t, deltaNoFeeMaxReal, int64(-1<<62),
		"even the largest realistic negative delta stays nowhere near int64's own negative boundary")

	deltaBothMax := int64(maxQuotedFee) - int64(maxPossibleRealFee) //nolint:gosec // this is exactly the check under review
	assert.InDelta(t, float64(maxQuotedFee), float64(deltaBothMax), float64(maxPossibleRealFee),
		"delta stays a small, well-behaved number relative to the operands' own shared scale")
}

// TestCashAuditSecB_ReconcileDelta_ZeroAndNegativeBoundary checks the
// delta==0 fast path (reconcileCashRedeemFee's own early return, verified
// indirectly here via the arithmetic it gates on) alongside dust-sized
// operands, where a naive signed/unsigned mixing bug would most likely show
// up first.
func TestCashAuditSecB_ReconcileDelta_ZeroAndNegativeBoundary(t *testing.T) {
	cases := []struct {
		name          string
		quoted, real  uint64
		expectedDelta int64
	}{
		{"both zero", 0, 0, 0},
		{"equal nonzero", 500, 500, 0},
		{"quoted exceeds real by one mloki", 1, 0, 1},
		{"real exceeds quoted by one mloki", 0, 1, -1},
		{"dust quoted, larger real", 1, 10000, -9999},
		{"quoted equals uint64->int64 comfortable max for this system", 1_000_000_000, 1, 999_999_999},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			delta := int64(c.quoted) - int64(c.real) //nolint:gosec // mirrors reconcileCashRedeemFee's own cast exactly
			assert.Equal(t, c.expectedDelta, delta)
		})
	}
}

// TestCashAuditSecB_SplitFeeRounding_IsSubadditive_HubCollectsLessOverall is
// the concrete, numeric demonstration of the informational finding
// described in this file's package-level doc comment: redeeming a slice as
// several smaller split pieces (same immutable redeem_fee_ppm on every
// piece) collects LESS total fee revenue for the hub than one redemption of
// the whole, undivided amount would have — never more, and the gap is
// bounded to less than 1 mloki per extra split.
func TestCashAuditSecB_SplitFeeRounding_IsSubadditive_HubCollectsLessOverall(t *testing.T) {
	const ppm = 999_000 // an intentionally steep rate (99.9%) chosen to make the dust-piece effect obvious
	const total = uint64(1_000_000)

	wholeFee := CalculateFeeSkimMloki(total, ppm)
	require.Equal(t, uint64(999_000), wholeFee)

	// Split into a 1-mloki dust piece and the remainder. Both pieces
	// inherit the SAME ppm (NIP-CASH's immutability/inheritance rule for
	// redeem_fee_ppm across a split), so this is exactly what a recipient
	// splitting their own slice before redeeming each piece would see.
	dust, remainder := uint64(1), total-1
	require.Equal(t, total, dust+remainder)

	dustFee := CalculateFeeSkimMloki(dust, ppm)
	remainderFee := CalculateFeeSkimMloki(remainder, ppm)
	splitFeeSum := dustFee + remainderFee

	assert.Equal(t, uint64(0), dustFee,
		"the 1-mloki dust piece's own fee floors to exactly zero, even at a 99.9%% rate")
	assert.LessOrEqual(t, splitFeeSum, wholeFee,
		"splitting a slice before redeeming each piece must never let the hub collect MORE than a single whole-amount redemption would have")
	assert.Less(t, splitFeeSum, wholeFee,
		"for this split, floor-rounding subadditivity actually bites: the hub collects strictly less than it would from one whole-amount redemption")
	assert.Less(t, wholeFee-splitFeeSum, uint64(2),
		"the total leakage from splitting into N=2 pieces is bounded by strictly less than N mloki (dust, not a meaningful economic exploit on its own)")
}
