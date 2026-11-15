package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// fundApp gives an app a real settled incoming balance, the same shape
// cashwallet.Commit's internal transfer leaves behind on a freshly minted
// bill.
func fundApp(t *testing.T, svc *tests.TestService, appID uint, amountMloki uint64) {
	t.Helper()
	id := appID
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:       &id,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_SETTLED,
		AmountMloki: amountMloki,
		PaymentHash: tests.RandomHex32(),
	}).Error)
}

// A hub that has minted its entire balance is fully solvent, and must not be
// reported as short.
//
// This is the normal end state of ordinary operation, and the previous
// coverage reading called it "Short — cannot cover every bill" on every such
// hub. Minting is not bookkeeping inside the hub: cashwallet.Commit does a
// real internal transfer, so the backing money leaves the hub's own ledger
// the instant a bill is minted and lives in that bill's app row. Comparing
// the hub's leftover balance against what its bills owe compares two pools
// that are disjoint by construction.
func TestCashCoverage_FullyMintedHubIsNotShort(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000_000, 3600)
	bill := newBareCashWallet(t, svc, hub, 10)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(bill.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 1_000_000},
	}))
	// The bill holds exactly what it owes; the hub itself holds nothing left.
	fundApp(t, svc, bill.ID, 1_000_000)

	stats, err := newTestAPI(svc).GetCashHubStats(hub.ID)
	require.NoError(t, err)

	assert.EqualValues(t, 1_000_000, stats.OutstandingMloki)
	assert.EqualValues(t, 1_000_000, stats.BackingMloki,
		"backing is what the bills hold, and it is what outstanding is a claim on")
	assert.Zero(t, stats.ShortfallMloki,
		"a hub whose bills are fully funded is solvent, however little the hub itself has left")
	assert.Zero(t, stats.BalanceMloki,
		"the hub's own balance is spare minting capacity here, and is deliberately not part of the comparison")
}

// The dangerous direction: one underfunded bill must be caught even when the
// hub is sitting on plenty.
//
// An aggregate reading cannot do this. A large unminted hub balance would
// swamp a single bill whose own ledger had fallen below its unclaimed slices
// and report a comfortable ratio — false assurance on exactly the failure a
// solvency figure exists to catch. So the shortfall is computed per bill and
// only deficits are summed.
func TestCashCoverage_UnderfundedBillIsCaughtDespiteHealthyAggregate(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000_000, 3600)

	healthy := newBareCashWallet(t, svc, hub, 10)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(healthy.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 500_000},
	}))
	fundApp(t, svc, healthy.ID, 900_000) // holds more than it owes

	broken := newBareCashWallet(t, svc, hub, 10)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(broken.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 400_000},
	}))
	fundApp(t, svc, broken.ID, 300_000) // 100,000 short

	stats, err := newTestAPI(svc).GetCashHubStats(hub.ID)
	require.NoError(t, err)

	assert.EqualValues(t, 900_000, stats.OutstandingMloki)
	assert.EqualValues(t, 1_200_000, stats.BackingMloki)
	assert.EqualValues(t, 100_000, stats.ShortfallMloki,
		"the surplus on one bill must not offset the deficit on another — only deficits are summed")

	// And the aggregate alone would have looked fine: backing exceeds
	// outstanding comfortably. That is the whole argument for measuring per
	// bill rather than in total.
	assert.Greater(t, stats.BackingMloki, stats.OutstandingMloki,
		"aggregate backing looks healthy, which is precisely why it cannot be the signal")
}
