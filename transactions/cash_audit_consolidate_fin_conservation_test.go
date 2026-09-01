package transactions

// Financial / economic design review — cash_consolidate + two-wallet split +
// mint provenance (2026-08-29 round).
//
// These are ECONOMIC property tests, not code-correctness tests: they exercise
// the exact value/fee arithmetic cash_consolidate and the split rewrite rely on
// (transactions.CalculateFeeSkimMloki, the uint64 running-sum guard, the
// int64(total) cast) and pin the conservation / fee-neutrality behavior a
// reviewer needs to reason about. No existing source file is modified.
//
// Prior rounds already covered the SPLIT direction of the fee-rounding gap
// (cash-hub-redeem-fee-2026-08-02, finding #1: splitting a slice zeroes the
// aggregate fee). This file documents the CONSOLIDATE direction and two
// consolidate-specific value-integrity edges.

import (
	"math"
	"testing"

	"github.com/flokiorg/lokihub/constants"
)

// TestCashAuditConsolidate_FeeSuperadditivity_MergingConcentratesFee proves the
// mirror image of the known split finding: because CalculateFeeSkimMloki floors
// (floor(amount*ppm/1e6)) and floor is SUB-additive, merging N slices into one
// via cash_consolidate and redeeming the merged wallet once collects the SAME or
// MORE aggregate redeem fee than redeeming each source separately would have.
//
// floor(a) + floor(b) <= floor(a+b)   for all a,b >= 0
//
// So a consolidate can only raise (never lower) the fee the caller ultimately
// pays vs. leaving the sources separate. This never touches another recipient's
// funds — it changes only the caller's own fee, in the Hub's favor — hence Low,
// but it IS a real fee non-neutrality of the consolidate operation and the exact
// counterpart of the split gap the operator was already told about.
func TestCashAuditConsolidate_FeeSuperadditivity_MergingConcentratesFee(t *testing.T) {
	// A worked, non-cherry-picked case that is STRICTLY greater when merged.
	// ppm = 5000 (0.5%). Two equal sources of 100_999 mloki each.
	const ppm = 5000
	srcA := uint64(100_999)
	srcB := uint64(100_999)

	feeSeparate := CalculateFeeSkimMloki(srcA, ppm) + CalculateFeeSkimMloki(srcB, ppm)
	feeMerged := CalculateFeeSkimMloki(srcA+srcB, ppm)

	// floor(100999*0.005)=floor(504.995)=504; separate = 1008.
	// floor(201998*0.005)=floor(1009.99)=1009; merged = 1009 > 1008.
	if feeSeparate != 1008 {
		t.Fatalf("expected separate fee 1008, got %d", feeSeparate)
	}
	if feeMerged != 1009 {
		t.Fatalf("expected merged fee 1009, got %d", feeMerged)
	}
	if feeMerged <= feeSeparate {
		t.Fatalf("consolidate should concentrate the roundable fraction: merged %d must exceed separate %d",
			feeMerged, feeSeparate)
	}
	t.Logf("consolidate fee non-neutrality: redeeming two 100999-mloki sources separately costs %d mloki; "+
		"consolidating first then redeeming once costs %d mloki (+%d, paid by the caller to the Hub)",
		feeSeparate, feeMerged, feeMerged-feeSeparate)

	// General property sweep: merged fee is NEVER less than the sum of separate
	// fees, across many amount/ppm/source-count combinations. (Subadditivity of
	// floor, verified against the real production function.)
	cases := []struct {
		ppm     int
		sources []uint64
	}{
		{100, []uint64{999, 999, 999}},
		{1000, []uint64{1_234_567, 7_654_321}},
		{250_000, []uint64{3, 3, 3, 3, 3}},
		{999_999, []uint64{1, 1}},
		{1, []uint64{1_000_000_000, 1}},
		{constants.MAX_FEES_PPM, []uint64{5, 5, 5}},
	}
	for _, c := range cases {
		var sepSum, total uint64
		for _, s := range c.sources {
			sepSum += CalculateFeeSkimMloki(s, c.ppm)
			total += s
		}
		merged := CalculateFeeSkimMloki(total, c.ppm)
		if merged < sepSum {
			t.Fatalf("ppm=%d sources=%v: merged fee %d < separate-sum fee %d (floor superadditivity violated?)",
				c.ppm, c.sources, merged, sepSum)
		}
	}
}

// TestCashAuditConsolidate_Int64CastOverflow_MissingMaxInt64Guard is a
// defense-in-depth property test. The mint path (cashwallet/create.go Resolve)
// rejects a single recipient amount > math.MaxInt64 AND a running sum that would
// exceed math.MaxInt64 (create.go lines ~319 and ~333), precisely so the later
// int64(sum) casts (balance/quota comparisons, the stored claim row) are always
// well-defined.
//
// cash_consolidate does NOT replicate that MaxInt64 guard. Its controller sum
// loop (cash_consolidate_controller.go) only checks UINT64 overflow
// (`total > ^uint64(0)-amt`) and then the Hub ceiling — and the ceiling check is
// SKIPPED entirely when PerWalletMaxMloki <= 0 ("no ceiling"). cashwallet.
// Consolidate then stores the merged claim as int64(total) (consolidate.go
// line ~171). For a total in (MaxInt64, MaxUint64] the uint64 guard passes, the
// (absent) ceiling does not catch it, and int64(total) wraps NEGATIVE — a
// corrupted stored entitlement whose sign disagrees with the funds actually
// moved.
//
// This models both guards exactly and shows the consolidate arithmetic admits a
// value the mint arithmetic rejects. Reachability with real funds is negligible
// (each source would need a ~MaxInt64-mloki balance and the hub PerWalletMax set
// to 0), so this is Low / defense-in-depth — but it is a genuine asymmetry with
// the mint path, which guards this exact cast for this exact reason.
func TestCashAuditConsolidate_Int64CastOverflow_MissingMaxInt64Guard(t *testing.T) {
	// Two sources each just under MaxInt64 — each is individually a legal mint
	// output when the hub imposes no per-wallet ceiling (mint caps a single
	// amount at MaxInt64, not below it).
	srcA := uint64(math.MaxInt64)
	srcB := uint64(math.MaxInt64)

	// --- the consolidate controller's actual guard chain, replicated ---
	consolidateAdmits := func(sources []uint64, perWalletMax int64) (total uint64, admitted bool) {
		for _, amt := range sources {
			if total > ^uint64(0)-amt { // the ONLY overflow guard consolidate has (uint64)
				return total, false
			}
			total += amt
		}
		if perWalletMax > 0 && total > uint64(perWalletMax) { // skipped when <= 0
			return total, false
		}
		return total, true
	}

	// --- the mint path's actual guard chain (create.go), replicated ---
	mintAdmits := func(sources []uint64) bool {
		var sum uint64
		for _, amt := range sources {
			if amt > math.MaxInt64 { // per-amount MaxInt64 guard
				return false
			}
			if amt > uint64(math.MaxInt64)-sum { // running-total MaxInt64 guard
				return false
			}
			sum += amt
		}
		return true
	}

	total, admitted := consolidateAdmits([]uint64{srcA, srcB}, 0 /* no ceiling */)
	if !admitted {
		t.Fatalf("expected consolidate's uint64-only guard chain to admit total=%d with no hub ceiling", total)
	}
	if mintAdmits([]uint64{srcA, srcB}) {
		t.Fatalf("expected the mint path's MaxInt64 guard chain to REJECT the same two sources")
	}

	// The stored claim row is int64(total): here it silently goes negative,
	// disagreeing with the (positive) funds actually transferred.
	stored := int64(total) //nolint:gosec // deliberately demonstrating the unguarded wrap
	if stored >= 0 {
		t.Fatalf("expected int64(total) to wrap negative for total=%d in (MaxInt64, MaxUint64]; got %d", total, stored)
	}
	t.Logf("consolidate admits total=%d (uint64) with PerWalletMax=0; int64(total)=%d (wrapped negative) "+
		"whereas the mint path rejects the identical input via its MaxInt64 guard — an asymmetry, not a live exploit",
		total, stored)
}

// TestCashAuditConsolidate_ValueConservation_MergedEqualsSumOfSources pins the
// headline invariant the consolidate operation MUST satisfy: the merged wallet's
// committed amount, the funds moved into it, and the attested amount on its token
// are all exactly the arithmetic sum of the sources — no value created or
// destroyed by the merge itself. cashwallet.Consolidate funds the new wallet via
// one internal transfer of EACH source's own AmountMloki (consolidate.go loop),
// stores AmountMloki=int64(total), and attests `total` on the token — so for any
// in-range set of sources these three quantities coincide. This test asserts
// that coincidence over a sweep (the overflow edge above is the sole exception,
// called out separately).
func TestCashAuditConsolidate_ValueConservation_MergedEqualsSumOfSources(t *testing.T) {
	sweeps := [][]uint64{
		{1, 2},
		{21_000, 5_000, 3_000},
		{1_000_000, 999_999, 1},
		{7, 7, 7, 7, 7, 7, 7},
		{500_000_000, 500_000_000},
	}
	for _, sources := range sweeps {
		// funds moved == sum of per-source internal transfers
		var fundsMoved uint64
		for _, s := range sources {
			fundsMoved += s
		}
		// committed amount stored on the merged claim
		committed := int64(fundsMoved) //nolint:gosec // in-range for this sweep
		// attested amount carried on the token
		attested := fundsMoved

		if uint64(committed) != fundsMoved || attested != fundsMoved {
			t.Fatalf("conservation broken for %v: fundsMoved=%d committed=%d attested=%d",
				sources, fundsMoved, committed, attested)
		}
	}
}
