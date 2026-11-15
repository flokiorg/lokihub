package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// The Cash Hubs list's overview is fed by this one endpoint. The hub count and
// the combined balance are the two figures on it the page cannot work out for
// itself: it filters its page of apps client-side, so anything it counted
// would be the current page's rather than the node's.
func TestGetAllCashHubStats_CountsEveryHubOnTheNode(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	theAPI := newTestAPI(svc)

	// Before anything exists: a real answer, not an error. This is what the
	// list renders on a fresh node.
	empty, err := theAPI.GetAllCashHubStats()
	require.NoError(t, err)
	assert.Zero(t, empty.HubsCount)
	assert.Zero(t, empty.BalanceMloki)
	assert.Zero(t, empty.OutstandingMloki)
	assert.NotEmpty(t, empty.Daily, "the chart needs its days even on an empty node")

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	second := tests.CreateCashHub(t, svc, 10_000, 3600)
	require.NotEqual(t, hub.ID, second.ID)

	wallet := newBareCashWallet(t, svc, hub, 10)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 7000},
	}))

	stats, err := theAPI.GetAllCashHubStats()
	require.NoError(t, err)
	assert.EqualValues(t, 2, stats.HubsCount, "both hubs must be counted, not just the one holding a bill")
	assert.EqualValues(t, 7000, stats.OutstandingMloki)
	assert.EqualValues(t, 7000, stats.IssuedMloki)

	// A cash_wallet is not a cash_hub. Counting children here would make the
	// figure grow with every bill minted, which is the opposite of what the
	// label says.
	assert.EqualValues(t, 2, stats.HubsCount,
		"a bill's own wallet must not be counted as a hub")
}

// The per-hub endpoint must not start reporting node-wide figures: its two
// extra fields stay zero, because a hub dashboard already has both on its own
// App row and a non-zero value there would be read as this hub's.
func TestGetCashHubStats_LeavesTheNodeWideFieldsZero(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	theAPI := newTestAPI(svc)

	stats, err := theAPI.GetCashHubStats(hub.ID)
	require.NoError(t, err)
	assert.Zero(t, stats.HubsCount)
	assert.Zero(t, stats.BalanceMloki)
}

// Guards the clock the aggregate is computed against: "now" has to be the
// call's own, or the live-slice expiry test in the outstanding query drifts.
func TestGetAllCashHubStats_UsesCurrentTimeForExpiry(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 10)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 3000},
	}))

	// Expire the bill in the past: an expired slice is no longer redeemable,
	// so it must leave the outstanding liability rather than sit in it.
	past := time.Now().Add(-time.Hour)
	require.NoError(t, svc.DB.Model(&db.App{}).
		Where("id = ?", wallet.ID).Update("expires_at", past).Error)

	stats, err := newTestAPI(svc).GetAllCashHubStats()
	require.NoError(t, err)
	assert.Zero(t, stats.OutstandingMloki,
		"an expired bill is not still redeemable, so it is not outstanding")
	assert.EqualValues(t, 3000, stats.IssuedMloki,
		"it was still issued, though — it just came back")
}
