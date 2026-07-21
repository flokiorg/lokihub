//go:build integration

// renewal_test.go extends circle_hub_test.go's
// CreateWallet_BudgetRenewalTighterThanFloor coverage of the hub's
// MinBudgetRenewal floor. That existing test only proves the tight end
// ("daily", budget_renewal's own tightest possible rank, is rejected
// against a "monthly" floor). Since every ephemeral hub here can be created
// with an EXACT, known MinBudgetRenewal (unlike a single fixed
// pre-provisioned hub, whose floor this suite previously had to treat as an
// opaque unknown), this file exercises the floor boundary precisely, for
// every possible floor value: exactly-at-floor is accepted, one notch
// tighter is rejected, and "never" (the loosest possible rank) is always
// accepted no matter the floor.
package integration

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// budgetRenewalsByRank mirrors constants.BudgetRenewalRank's ordering
// (constants/constants.go) - index i is one notch looser than index i-1.
var budgetRenewalsByRank = []string{
	constants.BUDGET_RENEWAL_DAILY,
	constants.BUDGET_RENEWAL_WEEKLY,
	constants.BUDGET_RENEWAL_MONTHLY,
	constants.BUDGET_RENEWAL_YEARLY,
	constants.BUDGET_RENEWAL_NEVER,
}

// createCircleWalletExpectingCode creates an ephemeral allowlist circle hub
// with the given MinBudgetRenewal floor, then attempts create_circle_wallet
// with the given budget_renewal - shared by every floor-boundary subtest
// below, which all differ only in which two values they compare.
func createCircleWalletExpectingCode(t *testing.T, cfg *Config, floor, requested, expectCode string) {
	t.Helper()
	privkey := newTestPrivkey(t)
	hub, _, _ := createEphemeralCircleHub(t, cfg, "renewal-floor", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{MinBudgetRenewal: floor})
	hubClient := mustConnect(t, hub.Connection)
	pubkey := mustPubkey(t, privkey)
	identityEvent := buildCircleWalletIdentityEvent(t, privkey, hubClient.ClientPubkey())

	var result CreateCircleWalletResult
	err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     happyPathAmountMloki,
		Expiry:        happyPathExpirySecs,
		BudgetRenewal: requested,
		IdentityEvent: eventJSON(t, identityEvent),
	}, &result)

	if expectCode == "" {
		require.NoError(t, err, "budget_renewal %q against floor %q must be accepted", requested, floor)
		require.Equal(t, requested, result.BudgetRenewal)
		return
	}
	requireNWCErrorCode(t, err, expectCode)
}

func TestCircleHub_CreateWallet_BudgetRenewal_ExactlyAtFloor_Accepted(t *testing.T) {
	cfg := requireConfig(t)
	for _, floor := range budgetRenewalsByRank {
		t.Run(floor, func(t *testing.T) {
			createCircleWalletExpectingCode(t, cfg, floor, floor, "")
		})
	}
}

func TestCircleHub_CreateWallet_BudgetRenewal_OneNotchTighterThanFloor_Rejected(t *testing.T) {
	cfg := requireConfig(t)
	for i := 1; i < len(budgetRenewalsByRank); i++ {
		floor := budgetRenewalsByRank[i]
		tighter := budgetRenewalsByRank[i-1]
		t.Run(floor, func(t *testing.T) {
			createCircleWalletExpectingCode(t, cfg, floor, tighter, constants.ERROR_BAD_REQUEST)
		})
	}
}

func TestCircleHub_CreateWallet_BudgetRenewalNever_AcceptedRegardlessOfFloor(t *testing.T) {
	cfg := requireConfig(t)
	for _, floor := range budgetRenewalsByRank {
		t.Run(floor, func(t *testing.T) {
			createCircleWalletExpectingCode(t, cfg, floor, constants.BUDGET_RENEWAL_NEVER, "")
		})
	}
}

// TestCircleHub_CreateWallet_BudgetRenewalNever_ReportsNoRenewsAt keeps the
// original single-scenario assertion on get_budget's own reported shape (not
// just that creation succeeds) - a "never"-renewal wallet must never report
// a renews_at timestamp, regardless of the hub's floor.
func TestCircleHub_CreateWallet_BudgetRenewalNever_ReportsNoRenewsAt(t *testing.T) {
	cfg := requireConfig(t)
	privkey := newTestPrivkey(t)
	hub, _, _ := createEphemeralCircleHub(t, cfg, "renewal-never-shape", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{MinBudgetRenewal: constants.BUDGET_RENEWAL_MONTHLY})
	hubClient := mustConnect(t, hub.Connection)
	pubkey := mustPubkey(t, privkey)
	identityEvent := buildCircleWalletIdentityEvent(t, privkey, hubClient.ClientPubkey())

	var result CreateCircleWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     happyPathAmountMloki,
		Expiry:        happyPathExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, identityEvent),
	}, &result))

	pairingURI, err := nwcclient.DecryptPairingURI(privkey, result.WalletPubkey, result.EncryptedPairingURI)
	require.NoError(t, err)
	child := mustConnect(t, pairingURI)

	var budget GetBudgetResult
	require.NoError(t, child.Call(ctxT(t), "get_budget", struct{}{}, &budget))
	require.Equal(t, constants.BUDGET_RENEWAL_NEVER, budget.RenewalPeriod)
	require.Nil(t, budget.RenewsAt, "a \"never\"-renewal wallet must report no renews_at timestamp at all")
}
