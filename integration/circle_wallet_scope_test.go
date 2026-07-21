//go:build integration

// circle_wallet_scope_test.go exercises what a circle_wallet child is
// actually allowed to do once it exists - receiving and spending real money
// within its own cap - as opposed to circle_hub_test.go, which only covers
// create_circle_wallet's own request-validation surface. Mints its own
// ephemeral hub and child (see mintCircleChild in cross_test.go) rather than
// sharing one with another test file.
package integration

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// scopeTestAmountMloki is deliberately small relative to happyPathAmountMloki
// (the wallet's own max_amount cap) - these scenarios run against a real,
// shared, persistent wallet that accumulates history across every suite run
// against a given hub, so each individual movement here must stay well under
// the cap even after many runs.
const scopeTestAmountMloki = 200

func TestCircleWalletChild_Scope(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCircleHub(t, cfg, "scope-circle-hub", circlePolicyAllowlist,
		[]string{newTestPrivkey(t)}, ephemeralCircleHubOpts{FundLoki: 100_000})
	testCircleWalletChildScope(t, cfg, hub)
}

// fundCircleChild has child mint a real invoice for amountMloki and the
// configured simple_wallet pay it - the external-funding half of the
// round trip these scope tests exercise. Skips cleanly (via
// payInvoiceFromSimpleWallet) if simple_wallet isn't configured or isn't
// granted pay_invoice scope.
func fundCircleChild(t *testing.T, cfg *Config, child *nwcclient.Client, amountMloki uint64, description string) MakeInvoiceResult {
	t.Helper()
	var invoice MakeInvoiceResult
	require.NoError(t, child.Call(ctxT(t), "make_invoice", MakeInvoiceParams{
		Amount:      amountMloki,
		Description: description,
	}, &invoice))
	require.NotEmpty(t, invoice.Invoice)

	payInvoiceFromSimpleWallet(t, cfg, invoice.Invoice)
	return invoice
}

func testCircleWalletChildScope(t *testing.T, cfg *Config, hub CircleHubConfig) {
	child := mintCircleChild(t, hub)

	// A circle_wallet child, unlike a jit_wallet child, must be able to
	// receive funds directly (make_invoice) from any external payer - not
	// just from another child of the same lokihub instance, as cross_test.go
	// already covers via a jit_wallet child paying a circle child's invoice.
	//
	// Withdraws the same amount back out at the end so later subtests in
	// this same function (which share this one child) don't inherit
	// residual balance that would eat into their own headroom assumptions.
	t.Run("FundedBySimpleWallet_BalanceIncreasesAndShowsInHistory", func(t *testing.T) {
		var before GetBalanceResult
		require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &before))

		invoice := fundCircleChild(t, cfg, child, scopeTestAmountMloki, "integration circle scope fund test")

		var after GetBalanceResult
		require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &after))
		require.EqualValues(t, before.Balance+int64(scopeTestAmountMloki), after.Balance)

		var txs ListTransactionsResult
		require.NoError(t, child.Call(ctxT(t), "list_transactions", ListTransactionsParams{Limit: 10}, &txs))
		require.True(t, containsPaymentHash(txs.Transactions, invoice.PaymentHash),
			"circle child's transaction history should show the incoming external payment")

		withdrawInvoice := mintInvoiceFromSimpleWallet(t, cfg, scopeTestAmountMloki, "integration circle scope fund-test cleanup withdraw")
		var withdrawResult PayInvoiceResult
		require.NoError(t, child.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: withdrawInvoice.Invoice}, &withdrawResult))
	})

	// The reverse direction: a circle_wallet child spending its own real
	// balance out to an external payee (pay_invoice), not just internally to
	// another child on the same instance.
	t.Run("WithdrawsToSimpleWallet_BalanceDecreasesAndShowsInHistory", func(t *testing.T) {
		// Self-funds first so this subtest doesn't depend on the funding
		// subtest above having already run (or on leftover balance from a
		// prior suite run) to have enough to withdraw.
		fundCircleChild(t, cfg, child, scopeTestAmountMloki, "integration circle scope withdraw-setup fund")

		var before GetBalanceResult
		require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &before))
		require.GreaterOrEqual(t, before.Balance, int64(scopeTestAmountMloki))

		invoice := mintInvoiceFromSimpleWallet(t, cfg, scopeTestAmountMloki, "integration circle scope withdraw test")

		var result PayInvoiceResult
		require.NoError(t, child.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: invoice.Invoice}, &result))
		require.NotEmpty(t, result.Preimage)

		var after GetBalanceResult
		require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &after))
		require.EqualValues(t, before.Balance-int64(scopeTestAmountMloki), after.Balance)

		var txs ListTransactionsResult
		require.NoError(t, child.Call(ctxT(t), "list_transactions", ListTransactionsParams{Limit: 10}, &txs))
		require.True(t, containsPaymentHash(txs.Transactions, invoice.PaymentHash),
			"circle child's transaction history should show the outgoing external payment")
	})

	// Distinguishes the two independent rejection reasons on pay_invoice:
	// this one is a real-balance shortfall, not the wallet's own max_amount
	// cap (see MakeInvoice_ExceedsPerWalletCap_QuotaExceededRejected below),
	// and must produce ERROR_INSUFFICIENT_BALANCE specifically.
	t.Run("PayInvoice_ExceedsRealBalance_InsufficientBalanceRejected", func(t *testing.T) {
		var balance GetBalanceResult
		require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &balance))

		tooMuch := uint64(balance.Balance) + 1_000_000
		invoice := mintInvoiceFromSimpleWallet(t, cfg, tooMuch, "integration circle scope overpay test")

		var result PayInvoiceResult
		err := child.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: invoice.Invoice}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_INSUFFICIENT_BALANCE)
	})

	// make_invoice_controller.go caps a circle wallet's incoming invoices
	// against its own max_amount ceiling (current balance + invoice amount
	// must not exceed it) - a lifetime hold-cap independent of budget_renewal
	// and distinct from the outgoing-spend cap circle_hub_test.go's
	// CreateWallet_MaxAmountExceedsPerWalletCap already covers at wallet
	// creation time. get_budget's TotalBudget reports the wallet's own
	// max_amount (available to a circle wallet child, unlike a jit wallet
	// child which has no get_budget access at all), so headroom is computed
	// from it rather than hardcoding the value configured at creation time.
	t.Run("MakeInvoice_ExceedsPerWalletCap_QuotaExceededRejected", func(t *testing.T) {
		var budget GetBudgetResult
		require.NoError(t, child.Call(ctxT(t), "get_budget", struct{}{}, &budget))
		var balance GetBalanceResult
		require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &balance))
		require.Greater(t, budget.TotalBudget, uint64(balance.Balance),
			"wallet must have some headroom left under its cap for this test to exercise the boundary meaningfully")

		headroom := budget.TotalBudget - uint64(balance.Balance)

		var result MakeInvoiceResult
		err := child.Call(ctxT(t), "make_invoice", MakeInvoiceParams{
			Amount:      headroom + 1,
			Description: "integration circle scope cap test",
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_QUOTA_EXCEEDED)
	})

	// get_budget is reachable for a circle_wallet child (unlike jit_wallet,
	// see integration/README.md) and must reflect real cumulative outgoing
	// spend, not just the cap it was created with. get_budget_controller.go/
	// GetBudgetUsageSat round-trip the real mloki sum through a sat-ish unit
	// (sum mloki, divide by 1000, then multiply back by 1000 for the wire
	// response - see db/queries/get_budget_usage.go), so used_budget is only
	// ever accurate to the nearest budgetTestAmountMloki, truncating down.
	// budgetTestAmountMloki must be an exact multiple of that 1000 unit so
	// the *delta* this subtest causes is exact regardless of any fractional
	// remainder already sitting below the rounding boundary from other
	// subtests' smaller (scopeTestAmountMloki) payments.
	const budgetTestAmountMloki = 1000
	t.Run("GetBudget_ReflectsRealSpendAfterPayment", func(t *testing.T) {
		fundCircleChild(t, cfg, child, budgetTestAmountMloki, "integration circle scope budget-setup fund")

		var before GetBudgetResult
		require.NoError(t, child.Call(ctxT(t), "get_budget", struct{}{}, &before))

		invoice := mintInvoiceFromSimpleWallet(t, cfg, budgetTestAmountMloki, "integration circle scope budget test")
		var payResult PayInvoiceResult
		require.NoError(t, child.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: invoice.Invoice}, &payResult))

		var after GetBudgetResult
		require.NoError(t, child.Call(ctxT(t), "get_budget", struct{}{}, &after))
		require.EqualValues(t, before.UsedBudget+budgetTestAmountMloki, after.UsedBudget,
			"get_budget must track this payment's real cost against used_budget")
	})
}
