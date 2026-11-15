package constants

import "testing"

// NIP-CW §Budget Renewal Floor ranks renewal periods tightest to loosest:
// daily, weekly, monthly, yearly, never. A Hub's floor refuses anything ranked
// tighter, so every adjacent pair must be strictly ordered: two periods sharing
// a rank would let a request one step too tight slip past the floor.
func TestBudgetRenewalRank_StrictlyTightestToLoosest(t *testing.T) {
	ordered := []string{
		BUDGET_RENEWAL_DAILY,
		BUDGET_RENEWAL_WEEKLY,
		BUDGET_RENEWAL_MONTHLY,
		BUDGET_RENEWAL_YEARLY,
		BUDGET_RENEWAL_NEVER,
	}
	for i := 1; i < len(ordered); i++ {
		tighter, looser := ordered[i-1], ordered[i]
		if BudgetRenewalRank(tighter) >= BudgetRenewalRank(looser) {
			t.Errorf("%s (rank %d) must rank tighter than %s (rank %d)",
				tighter, BudgetRenewalRank(tighter), looser, BudgetRenewalRank(looser))
		}
	}
	if BudgetRenewalRank(BUDGET_RENEWAL_DAILY) < 0 {
		t.Errorf("a known period must have a non-negative rank")
	}
	if got := BudgetRenewalRank("fortnightly"); got != -1 {
		t.Errorf("an unknown period must rank -1, got %d", got)
	}
}
