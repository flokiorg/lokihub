//go:build integration

// budget_test.go probes what a circle wallet's max_amount/budget actually
// bounds, beyond what circle_hub_test.go (creation-time validation) and
// circle_wallet_scope_test.go (make_invoice's incoming holdings ceiling,
// get_budget reflecting one payment's real cost, pay_invoice's separate
// real-balance check) already cover: pay_invoice's OWN quota check against
// cumulative used_budget, distinct from both of those, across every
// combination of circle policy and budget_renewal period.
package integration

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// TestCircleWallet_Budget_PayInvoiceQuotaExceeded_DistinctFromInsufficientBalance
// is the centerpiece finding of this file: max_amount/get_budget's
// total_budget is a genuine, enforced cumulative SPEND ceiling for
// pay_invoice, not just make_invoice's incoming holdings ceiling. It lives
// in transactions_service.go's shared payment path (validateCanPay's budget
// cap check against queries.GetBudgetUsageSat), one layer below
// pay_invoice_controller.go itself, and returns ERROR_QUOTA_EXCEEDED -
// deliberately distinct from ERROR_INSUFFICIENT_BALANCE
// (circle_wallet_scope_test.go's PayInvoice_ExceedsRealBalance test), even
// though both are "pay_invoice failed" - a caller needs to tell "you're out
// of allowance this period" apart from "you're out of money" to react
// correctly (wait for renewal vs. top up).
//
// Drives the wallet through two payments comfortably under max_amount
// (proving normal spend works and accumulates in used_budget), then a third
// payment that real balance alone would happily cover but cumulative spend
// would not. Run against both circle policies: the budget cap is enforced
// identically regardless of who's authorized to hold the wallet.
func TestCircleWallet_Budget_PayInvoiceQuotaExceeded_DistinctFromInsufficientBalance(t *testing.T) {
	cfg := requireConfig(t)
	for _, policy := range []string{circlePolicyAllowlist, circlePolicyFollowing} {
		t.Run(policy, func(t *testing.T) {
			testBudgetQuotaExceeded(t, cfg, policy)
		})
	}
}

func testBudgetQuotaExceeded(t *testing.T, cfg *Config, policy string) {
	privkey := newTestPrivkey(t)
	const maxAmountMloki = 5000
	const paymentAmountMloki = 2000 // exact multiple of 1000 - see GetBudgetUsageSat's sat-rounding note in circle_wallet_scope_test.go

	hub, _, _ := createEphemeralCircleHub(t, cfg, "budget-quota-"+policy, policy, []string{privkey},
		ephemeralCircleHubOpts{FundLoki: 100_000})
	hubClient := mustConnect(t, hub.Connection)
	pubkey := mustPubkey(t, privkey)
	identityEvent := buildCircleWalletIdentityEvent(t, privkey, hubClient.ClientPubkey())

	var created CreateCircleWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     maxAmountMloki,
		Expiry:        happyPathExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, identityEvent),
	}, &created))

	pairingURI, err := nwcclient.DecryptPairingURI(privkey, created.WalletPubkey, created.EncryptedPairingURI)
	require.NoError(t, err)
	child := mustConnect(t, pairingURI)

	spendOnce := func(description string) {
		fundCircleChild(t, cfg, child, paymentAmountMloki, description+" (fund)")
		invoice := mintInvoiceFromSimpleWallet(t, cfg, paymentAmountMloki, description+" (spend)")
		var result PayInvoiceResult
		require.NoError(t, child.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: invoice.Invoice}, &result))
	}

	// Two payments of 2000 each = 4000 cumulative, still under the 5000 cap.
	spendOnce("integration budget-quota test cycle 1")
	spendOnce("integration budget-quota test cycle 2")

	var budgetBefore GetBudgetResult
	require.NoError(t, child.Call(ctxT(t), "get_budget", struct{}{}, &budgetBefore))
	require.EqualValues(t, 2*paymentAmountMloki, budgetBefore.UsedBudget)
	require.EqualValues(t, maxAmountMloki, budgetBefore.TotalBudget)

	// Fund comfortably more than the next payment needs, so a rejection here
	// can only be the budget cap - real balance will not be the bottleneck.
	fundCircleChild(t, cfg, child, paymentAmountMloki+1000, "integration budget-quota test cycle 3 (fund)")
	var balance GetBalanceResult
	require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &balance))
	require.GreaterOrEqual(t, balance.Balance, int64(paymentAmountMloki), "real balance must comfortably cover the next payment on its own")

	invoice := mintInvoiceFromSimpleWallet(t, cfg, paymentAmountMloki, "integration budget-quota test cycle 3 (over-cap spend)")
	var result PayInvoiceResult
	err = child.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: invoice.Invoice}, &result)
	requireNWCErrorCode(t, err, constants.ERROR_QUOTA_EXCEEDED)

	var budgetAfter GetBudgetResult
	require.NoError(t, child.Call(ctxT(t), "get_budget", struct{}{}, &budgetAfter))
	require.EqualValues(t, budgetBefore.UsedBudget, budgetAfter.UsedBudget, "a rejected payment must not itself count against used_budget")
}

// TestCircleWallet_Budget_RenewsAt_MatchesConfiguredPeriod exercises
// get_budget's renews_at/renewal_period across every non-"never" period,
// something a single fixed pre-provisioned hub could never parametrize -
// db/queries/get_budget_usage.go's GetBudgetRenewsAt computes renews_at as
// the start of the NEXT calendar period (day/week/month/year), so this
// checks the reported timestamp is sane (in the future, within the period's
// own max real-world length) rather than pinning an exact value that would
// be racy against the real clock.
func TestCircleWallet_Budget_RenewsAt_MatchesConfiguredPeriod(t *testing.T) {
	cfg := requireConfig(t)

	periods := map[string]int64{
		constants.BUDGET_RENEWAL_DAILY:   2 * 24 * 3600,
		constants.BUDGET_RENEWAL_WEEKLY:  8 * 24 * 3600,
		constants.BUDGET_RENEWAL_MONTHLY: 32 * 24 * 3600,
		constants.BUDGET_RENEWAL_YEARLY:  367 * 24 * 3600,
	}
	for renewal, maxSecsAhead := range periods {
		t.Run(renewal, func(t *testing.T) {
			privkey := newTestPrivkey(t)
			hub, _, _ := createEphemeralCircleHub(t, cfg, "budget-renews-at-"+renewal, circlePolicyAllowlist, []string{privkey},
				ephemeralCircleHubOpts{MinBudgetRenewal: constants.BUDGET_RENEWAL_DAILY, FundLoki: 10_000})
			hubClient := mustConnect(t, hub.Connection)
			pubkey := mustPubkey(t, privkey)
			identityEvent := buildCircleWalletIdentityEvent(t, privkey, hubClient.ClientPubkey())

			var created CreateCircleWalletResult
			require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
				Pubkey:        pubkey,
				MaxAmount:     happyPathAmountMloki,
				Expiry:        happyPathExpirySecs,
				BudgetRenewal: renewal,
				IdentityEvent: eventJSON(t, identityEvent),
			}, &created))
			require.Equal(t, renewal, created.BudgetRenewal)

			pairingURI, err := nwcclient.DecryptPairingURI(privkey, created.WalletPubkey, created.EncryptedPairingURI)
			require.NoError(t, err)
			child := mustConnect(t, pairingURI)

			var budget GetBudgetResult
			require.NoError(t, child.Call(ctxT(t), "get_budget", struct{}{}, &budget))
			require.Equal(t, renewal, budget.RenewalPeriod)
			require.NotNil(t, budget.RenewsAt, "a non-\"never\" renewal must report a concrete renews_at timestamp")
			now := time.Now().Unix()
			require.Greater(t, int64(*budget.RenewsAt), now, "renews_at must be in the future")
			require.Less(t, int64(*budget.RenewsAt), now+maxSecsAhead, fmt.Sprintf("renews_at must fall within one %s period from now", renewal))
		})
	}
}
