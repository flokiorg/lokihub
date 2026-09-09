package cashwallet

// cash_redeem's real payment leg runs through
// transactions.validateCanPay's generic budget-cap gate
// (AppPermission.MaxAmountLoki), not only the exact, mloki-precise isolated-
// balance check (db/queries.GetIsolatedBalance). cashwallet.Commit/Split/
// Consolidate all derive maxAmountLoki via `sum/1000` (integer division —
// create.go:468, create.go:675, consolidate.go:123), so whenever a wallet's
// committed total isn't an exact multiple of 1000 mloki, the stored
// MaxAmountLoki is strictly less than the wallet's real total, by up to 999
// mloki.
//
// That floor-division looks like it could reject or strand a slice's tail
// end. It can't: the budget-cap check (transactions_service.go ~line 1494,
// ~1506) is `floor(amountWithFeeReserve/1000) + floor(budgetUsageSoFar/1000)
// > MaxAmountLoki`, where MaxAmountLoki is `floor(totalFundedMloki/1000)` and
// budgetUsageSoFar is a single floor over the exact-mloki sum of every prior
// outgoing transaction (db/queries/get_budget_usage.go), not a per-
// transaction floor accumulated across calls. Because floor(a)+floor(b) <=
// floor(a+b) for any nonnegative a, b, and the isolated-balance check already
// guarantees currentAmount+priorSum <= totalFundedMloki, the budget-cap
// check's left-hand side is always <= MaxAmountLoki: it's mathematically
// incapable of firing for any sequence of redemptions that sum to no more
// than the wallet's real funded total. This file proves that with real
// numbers through the real production path (cashwallet.Create ->
// transactions.SendPaymentSync -> validateCanPay), not just the arithmetic.
//
// Note: this makes the MaxAmountLoki budget-cap gate vestigial for a
// cash_wallet's CASH_REDEEM_SCOPE permission — unconditionally weaker than
// the exact-mloki isolated-balance check that already runs first in the same
// function, for every input this system can produce, and it burns one extra
// query (GetBudgetUsageSat) per external cash_redeem for a check that can
// never fire. Either drop the check for isolated-kind apps (already covered
// by GetIsolatedBalance) or, if kept for non-cash isolated kinds, ceil-divide
// (or enforce the cap in mloki directly) so the two gates agree on what "the
// ceiling" means.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
)

// TestAuditFin_MaxAmountLoki_FlooredFromMloki_NeverBlocksFullLegitimateRedeem
// builds a cash_wallet app funded with a deliberately NON-round-loki total
// (21999 mloki -- not a multiple of 1000) and a CASH_REDEEM_SCOPE
// AppPermission stamped with MaxAmountLoki = 21 -- EXACTLY what
// apps_service.go's saveAppTx (`utils.ClampUint64ToInt(maxAmountLoki)`, fed
// `sum/1000` by cashwallet.Commit/Split/Consolidate) would store for a real
// wallet funded with this same 21999-mloki total. Setup is built directly
// (mirroring transactions/cash_redeem_fee_reconciliation_test.go's own
// newCashHubAndWallet helper) rather than by driving the real mint path,
// because the mock LN client's MakeInvoice ignores the requested amount --
// a known test-infra limitation that would make
// fundInternal move the WRONG amount in this unit-test environment and
// confound the very check this test targets. This still exercises the REAL
// transactions.SendPaymentSync -> validateCanPay code path unchanged --
// only the funding step is short-circuited.
//
// Then it proves a real, full-amount external redemption for the entire
// 21999 mloki -- MORE than the nominal 21000-mloki-equivalent cap implies is
// payable -- still succeeds via the exact same SendPaymentSync call
// cash_redeem_controller.go's step 10 makes.
func TestAuditFin_MaxAmountLoki_FlooredFromMloki_NeverBlocksFullLegitimateRedeem(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const total = uint64(21_999) // NOT a multiple of 1000 mloki

	hub := &db.App{Name: "cash-hub", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCashHub}
	require.NoError(t, svc.DB.Create(hub).Error)
	wallet := &db.App{
		Name:        "cash-wallet",
		AppPubkey:   tests.RandomHex32(),
		Kind:        db.AppKindCashWallet,
		ParentAppID: &hub.ID,
		ParentKind:  db.ParentKindCash,
	}
	require.NoError(t, svc.DB.Create(wallet).Error)
	walletID := wallet.ID

	// The budget-cap permission row, stamped with EXACTLY what production
	// derives for a 21999-mloki wallet: floor(21999/1000) == 21.
	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId:         walletID,
		Scope:         constants.CASH_REDEEM_SCOPE,
		MaxAmountLoki: 21,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
	}).Error)
	require.NoError(t, svc.DB.Create(&db.CashWalletClaim{
		WalletAppID:   walletID,
		IdentityType:  db.CashIdentityBearer,
		IdentityValue: tests.RandomHex32(),
		AmountMloki:   int64(total), //nolint:gosec // test-only, small constant
	}).Error)
	tests.FundApp(svc, walletID, total, "fund-maxamountloki-truncation")

	require.Equal(t, int64(total), queries.GetIsolatedBalance(svc.DB, walletID),
		"the wallet's real ledger balance is exact mloki, matching the committed slice")

	// Redeem the FULL 21999 mloki in one shot -- more mloki than the stored
	// 21-loki (21000-mloki-equivalent) cap alone would suggest is payable.
	// Uses the exact SendPaymentSync call, and exact cash_claim_slice/
	// cash_redeem_fee_mloki metadata shape, cash_redeem_controller.go's own
	// step 10 sends -- not a synthetic shortcut.
	metadata := map[string]interface{}{
		"cash_claim_slice":      true,
		"cash_redeem_fee_mloki": uint64(0), // RedeemFeePpm defaults to 0 on a freshly hub-minted slice
	}
	amount := total
	txnSvc := newTestDeps(svc).TransactionsService
	transaction, err := txnSvc.SendPaymentSync(
		tests.MockZeroAmountInvoice, &amount, metadata, svc.LNClient, &walletID, nil,
	)
	require.NoError(t, err,
		"FINDING would be a real bug here if this failed: the loki-floored MaxAmountLoki cap "+
			"(21, i.e. 21000-mloki-equivalent) must never reject a legitimate full redemption of the "+
			"wallet's real 21999-mloki total -- and it doesn't, confirming the check is mathematically "+
			"subsumed by the isolated-balance check that already runs first")
	require.NotNil(t, transaction)

	assert.Equal(t, int64(0), queries.GetIsolatedBalance(svc.DB, walletID),
		"the full 21999 mloki was paid out -- the loki-truncated budget cap neither blocked it nor left any dust behind")
}

// TestAuditFin_MaxAmountLoki_FlooredCap_MathematicallyCannotExceedFundedTotal
// is the general, DB-free proof behind the concrete case above: for ANY
// nonnegative current-payment amount and prior-usage sum whose REAL mloki
// total does not exceed a wallet's real funded total, the floor-based budget
// check's own two floored terms can never sum to more than
// floor(fundedTotal/1000) -- so it can never fire against a legitimate
// sequence of redemptions, regardless of how many pieces the total is split
// across or where the /1000 boundaries fall.
func TestAuditFin_MaxAmountLoki_FlooredCap_MathematicallyCannotExceedFundedTotal(t *testing.T) {
	cases := []struct {
		name             string
		fundedTotalMloki uint64
		currentMloki     uint64
		priorUsageMloki  uint64
	}{
		{"single dust-under-boundary total, redeemed whole", 999, 999, 0},
		{"total 1 mloki over a loki boundary, redeemed whole", 1_001, 1_001, 0},
		{"total just under next boundary, two prior redemptions summing to it", 2_999, 999, 2_000},
		{"many small pieces each individually flooring to zero", 4_999, 999, 4_000},
		{"large non-round total, final redemption drains the rest", 21_999, 1_999, 20_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.LessOrEqual(t, c.currentMloki+c.priorUsageMloki, c.fundedTotalMloki,
				"test setup invariant: this case must represent funds actually available on the wallet")
			nominalCap := c.fundedTotalMloki / 1000                  // mirrors saveAppTx's own MaxAmountLoki derivation
			checkLHS := c.currentMloki/1000 + c.priorUsageMloki/1000 // mirrors validateCanPay's own check exactly
			assert.LessOrEqual(t, checkLHS, nominalCap,
				"floor(current/1000) + floor(priorUsage/1000) must never exceed floor(fundedTotal/1000) "+
					"whenever current+priorUsage <= fundedTotal -- the budget-cap check can never wrongfully "+
					"reject a legitimate redemption sequence")
		})
	}
}
