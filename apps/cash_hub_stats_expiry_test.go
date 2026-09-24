package apps_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// A slice whose redeem window has passed but which the sweep has not yet
// collected is NOT outstanding.
//
// It is unclaimed, so the obvious query counts it — but it is no longer
// redeemable (permissions.HasPermission rejects it on the wallet's ExpiresAt),
// and the union already reports it as 'expired' and sums it into Returned. So
// counting it as outstanding both contradicts the figure's own meaning and
// double-counts it, leaving Issued unequal to the sum of its parts.
func TestGetCashHubStats_ExpiredButUnsweptIsNotOutstanding(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	now := time.Now()
	hub := newCashHub(t, svc, 1_000_000, 3600)

	// A live bill whose window closed an hour ago, still holding an unclaimed
	// slice because the sweep has not run.
	past := now.Add(-time.Hour)
	expiredBill, _, err := svc.AppsService.CreateApp(
		"expired-bill", "", 0, constants.BUDGET_RENEWAL_NEVER, &past,
		[]string{constants.CASH_REDEEM_SCOPE}, db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(expiredBill.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 700},
	}))

	// A live bill still inside its window: the genuine liability.
	future := now.Add(time.Hour)
	liveBill, _, err := svc.AppsService.CreateApp(
		"live-bill", "", 0, constants.BUDGET_RENEWAL_NEVER, &future,
		[]string{constants.CASH_REDEEM_SCOPE}, db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(liveBill.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 300},
	}))

	stats, err := svc.AppsService.GetCashHubStats(hub.ID, now)
	require.NoError(t, err)

	assert.EqualValues(t, 300, stats.OutstandingMloki,
		"only the slice still inside its window is a live liability")
	assert.EqualValues(t, 1, stats.OutstandingCount)
	assert.EqualValues(t, 700, stats.ReturnedMloki,
		"the lapsed slice is counted once, as returned")

	// The identity that the double-count used to break.
	assert.EqualValues(t,
		stats.OutstandingMloki+stats.RedeemedMloki+stats.SplitMloki+
			stats.ReturnedMloki+stats.WrittenOffMloki,
		stats.IssuedMloki,
		"every slice must land in exactly one bucket")
}

// The daily series needs its own split and written-off buckets, or a client
// reconstructing the outstanding curve from it overstates that curve by every
// split and every write-off. 'returned' is correspondingly limited to
// expired/reclaimed, matching what Returned means in the totals.
func TestGetCashHubStats_DailySeriesSeparatesSplitAndWrittenOff(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	now := time.Now().UTC()
	hub := newCashHub(t, svc, 1_000_000, 3600)
	day := now.AddDate(0, 0, -1)

	// ClaimedAt is set ONLY on the split row, and that is load-bearing rather
	// than incidental. DeriveSliceOutcome assigns written-off and expired
	// exclusively in its ClaimedAt == nil branch — by construction a slice
	// that expired or was written off was never claimed, which is what
	// distinguishes it from redeemed/split.
	//
	// This fixture previously set ClaimedAt on all three, a state production
	// cannot produce, and it was the only reason the assertions below passed:
	// the daily series read those two buckets through a `claimed_at IS NOT
	// NULL` filter that never matched real rows, so both were structurally
	// zero on every hub while this test reported them working. Keep the
	// fixture honest and the test keeps its meaning.
	for _, a := range []db.CashBillSliceArchive{
		{AmountMloki: 4000, Outcome: db.CashSliceStatusSplit, CreatedAt: day, ClaimedAt: &day, ArchivedAt: now},
		{AmountMloki: 1500, Outcome: db.CashSliceStatusWrittenOff, CreatedAt: day, ArchivedAt: day},
		{AmountMloki: 900, Outcome: db.CashSliceStatusExpired, CreatedAt: day, ArchivedAt: day},
	} {
		a.WalletAppID = 900_000 + uint(a.AmountMloki) //nolint:gosec // fixed positive test amounts
		a.HubAppID = hub.ID
		a.ClaimID = 1
		a.IdentityType = db.CashIdentityPubkey
		a.IdentityValue = randomHex32()
		require.NoError(t, svc.DB.Create(&a).Error)
	}

	stats, err := svc.AppsService.GetCashHubStats(hub.ID, now)
	require.NoError(t, err)

	byDay := map[string]apps.CashHubDailyPoint{}
	for _, p := range stats.Daily {
		byDay[p.Date.Format("2006-01-02")] = p
	}
	point := byDay[day.Format("2006-01-02")]

	assert.EqualValues(t, 4000, point.SplitMloki, "a split gets its own bucket")
	assert.EqualValues(t, 1500, point.WrittenOffMloki, "a write-off gets its own bucket")
	assert.EqualValues(t, 900, point.ReturnedMloki,
		"returned is expired/reclaimed only — write-offs are no longer folded in")

	// The regression guard: these two are keyed on archived_at, the moment the
	// value actually came back, and must be non-zero for a real (unclaimed)
	// expired or written-off slice. Both were permanently zero before.
	assert.NotZero(t, point.WrittenOffMloki, "a write-off must reach the series at all")
	assert.NotZero(t, point.ReturnedMloki, "a return must reach the series at all")
}
