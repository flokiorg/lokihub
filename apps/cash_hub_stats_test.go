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

// TestGetCashHubStats_TotalsMoneyAcrossLiveAndArchived is the point of the
// whole archive from an operator's side: these figures span bills that no
// longer exist, which was impossible before their history outlived deletion.
func TestGetCashHubStats_TotalsMoneyAcrossLiveAndArchived(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	bill, _, err := svc.AppsService.CreateApp(
		"cash-bill", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE}, db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)

	now := time.Now()
	claimed := now.Add(-2 * time.Hour)
	settled := claimed.Add(time.Minute)
	// Both redeemed slices are minted exactly one minute before they settle,
	// so the median below is a known value rather than an artefact of when the
	// fixture happened to run.
	minted := settled.Add(-time.Minute)

	// Live: one still-redeemable slice (the liability) and one already
	// redeemed on a bill kept alive by its sibling.
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(bill.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 2000,
			ClaimedAt: &claimed, SettledAt: &settled, RedeemFeeMloki: 20, PaymentHash: randomHex32()},
	}))
	// created_at is set by gorm on insert, so it has to be pushed back
	// explicitly for the mint-to-settle span to be the one under test.
	require.NoError(t, svc.DB.Model(&db.CashWalletClaim{}).
		Where("wallet_app_id = ? AND settled_at IS NOT NULL", bill.ID).
		Update("created_at", minted).Error)

	// Archived: a redeemed slice, a split one, and an expired one from bills
	// that are gone.
	for _, a := range []db.CashBillSliceArchive{
		{AmountMloki: 4000, Outcome: db.CashSliceStatusRedeemed, RedeemFeeMloki: 40,
			CreatedAt: minted, ClaimedAt: &claimed, SettledAt: &settled},
		{AmountMloki: 8000, Outcome: db.CashSliceStatusSplit, CreatedAt: now.Add(-3 * time.Hour)},
		{AmountMloki: 500, Outcome: db.CashSliceStatusExpired, CreatedAt: now.Add(-3 * time.Hour), ClaimedAt: &claimed},
		// Void must not reach any total — no recipient ever saw that bill.
		{AmountMloki: 99999, Outcome: db.CashSliceStatusVoid, CreatedAt: now.Add(-3 * time.Hour)},
	} {
		a.WalletAppID = 500_000 + uint(a.AmountMloki) //nolint:gosec // fixed positive amounts in a test fixture
		a.HubAppID = hub.ID
		a.ClaimID = 1
		a.IdentityType = db.CashIdentityPubkey
		a.IdentityValue = randomHex32()
		a.ArchivedAt = now
		require.NoError(t, svc.DB.Create(&a).Error)
	}

	stats, err := svc.AppsService.GetCashHubStats(hub.ID, now)
	require.NoError(t, err)

	assert.EqualValues(t, 1000, stats.OutstandingMloki, "only the still-unclaimed live slice is a liability")
	assert.EqualValues(t, 1, stats.OutstandingCount)

	assert.EqualValues(t, 6000, stats.RedeemedMloki, "live 2000 + archived 4000")
	assert.EqualValues(t, 2, stats.RedeemedCount)
	assert.EqualValues(t, 8000, stats.SplitMloki, "value that moved to another bill, not out of the hub")
	assert.EqualValues(t, 500, stats.ReturnedMloki)
	assert.EqualValues(t, 60, stats.FeesEarnedMloki, "the hub's own cut, live 20 + archived 40")

	assert.EqualValues(t, 15500, stats.IssuedMloki, "1000+2000+4000+8000+500, with void excluded")
	assert.Zero(t, stats.WrittenOffMloki)

	require.NotNil(t, stats.MedianTimeToRedeemSecs)
	assert.EqualValues(t, 60, *stats.MedianTimeToRedeemSecs, "both redemptions took a minute from mint to settle")
}

func TestGetCashHubStats_DailySeriesBucketsFlow(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)

	now := time.Now().UTC()
	twoDaysAgo := now.AddDate(0, 0, -2)
	settled := now.AddDate(0, 0, -1)

	a := db.CashBillSliceArchive{
		WalletAppID: 700_001, HubAppID: hub.ID, ClaimID: 1,
		IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(),
		AmountMloki: 3000, Outcome: db.CashSliceStatusRedeemed,
		CreatedAt: twoDaysAgo, SettledAt: &settled, ArchivedAt: now,
	}
	require.NoError(t, svc.DB.Create(&a).Error)

	stats, err := svc.AppsService.GetCashHubStats(hub.ID, now)
	require.NoError(t, err)

	byDay := map[string]apps.CashHubDailyPoint{}
	for _, p := range stats.Daily {
		byDay[p.Date.Format("2006-01-02")] = p
	}
	assert.EqualValues(t, 3000, byDay[twoDaysAgo.Format("2006-01-02")].IssuedMloki,
		"issued is keyed on when the slice was minted")
	assert.EqualValues(t, 3000, byDay[settled.Format("2006-01-02")].RedeemedMloki,
		"redeemed is keyed on when the payout settled, which can be a different day")
	assert.NotEmpty(t, stats.Daily, "the window is filled even where nothing happened, so a chart has no gaps")
}

// TestGetCashHubStats_EmptyHub: a hub that has never minted must report zeroes
// rather than fail, since the dashboard renders before anything exists.
func TestGetCashHubStats_EmptyHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	stats, err := svc.AppsService.GetCashHubStats(hub.ID, time.Now())
	require.NoError(t, err)

	assert.Zero(t, stats.OutstandingMloki)
	assert.Zero(t, stats.IssuedMloki)
	assert.Nil(t, stats.MedianTimeToRedeemSecs, "nothing redeemed yet means no median, not zero")
	assert.NotEmpty(t, stats.Daily)
}

// TestGetCashHubStats_DailySeriesUTCMidnightBoundary guards the day-bucketing
// expression the daily series aggregates with.
//
// Bucketing moved from a Go map into SQL, so the database now decides which
// calendar day an event belongs to. On sqlite that is done by slicing the leading
// ten characters of the stored timestamp, because the driver writes a time.Time
// as Go's String() rendering, which sqlite's date() cannot parse at all. On
// postgres it is to_char with an explicit AT TIME ZONE 'UTC', since to_char on a
// timestamptz otherwise renders in the session zone.
//
// Either could be off by a day without anything erroring, so this pins two events
// one second either side of a UTC midnight and requires them in different
// buckets.
func TestGetCashHubStats_DailySeriesUTCMidnightBoundary(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)

	now := time.Now().UTC()
	// A midnight comfortably inside the reported window.
	midnight := now.AddDate(0, 0, -3).Truncate(24 * time.Hour)
	justBefore := midnight.Add(-time.Second)
	justAfter := midnight.Add(time.Second)

	for i, at := range []time.Time{justBefore, justAfter} {
		require.NoError(t, svc.DB.Create(&db.CashBillSliceArchive{
			WalletAppID: uint(710_001 + i), HubAppID: hub.ID, ClaimID: uint(10 + i),
			IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(),
			AmountMloki: int64(100 * (i + 1)), Outcome: db.CashSliceStatusRedeemed,
			CreatedAt: at, SettledAt: &at, ArchivedAt: now,
		}).Error)
	}

	stats, err := svc.AppsService.GetCashHubStats(hub.ID, now)
	require.NoError(t, err)

	byDay := map[string]apps.CashHubDailyPoint{}
	for _, p := range stats.Daily {
		byDay[p.Date.Format("2006-01-02")] = p
	}

	assert.EqualValues(t, 100, byDay[justBefore.Format("2006-01-02")].IssuedMloki,
		"a second before UTC midnight belongs to the earlier day")
	assert.EqualValues(t, 200, byDay[justAfter.Format("2006-01-02")].IssuedMloki,
		"a second after UTC midnight belongs to the later day")
}

// TestGetCashHubStats_DailySeriesSumsSameDay covers what moving the aggregation
// into SQL actually changed: several events on one day must be summed by the
// GROUP BY, not overwrite each other.
func TestGetCashHubStats_DailySeriesSumsSameDay(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)

	now := time.Now().UTC()
	// Truncated to midnight, the same way the midnight-boundary test above does
	// it, because the hourly offsets below must not cross a date boundary.
	//
	// This used to be a bare AddDate(0, 0, -2), which inherits now's HOUR, so
	// `day` sat at whatever time of day the suite happened to run. The loop then
	// seeded events at day+0h, +1h and +2h and asserted all three land on day's
	// calendar date — true for 22 hours out of 24, and false for a run starting
	// after ~22:00 UTC, when +1h/+2h roll into the next day and the same-day sum
	// comes up short. Caught 2026-10-01 at ~23:00 UTC by a full-suite run that
	// crossed midnight; it passes again on the next run, which is exactly what
	// makes this kind of test bug survive.
	day := now.AddDate(0, 0, -2).Truncate(24 * time.Hour)

	amounts := []int64{1000, 2500, 400}
	for i, amount := range amounts {
		at := day.Add(time.Duration(i) * time.Hour)
		// The invariant the assertions below depend on, pinned rather than
		// assumed: every seeded event is on day's own date.
		require.Equal(t, day.Format("2006-01-02"), at.Format("2006-01-02"),
			"event %d was seeded on a different calendar day than the one being summed", i)
		require.NoError(t, svc.DB.Create(&db.CashBillSliceArchive{
			WalletAppID: uint(720_001 + i), HubAppID: hub.ID, ClaimID: uint(20 + i),
			IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(),
			AmountMloki: amount, Outcome: db.CashSliceStatusRedeemed,
			CreatedAt: at, SettledAt: &at, ArchivedAt: now,
		}).Error)
	}

	stats, err := svc.AppsService.GetCashHubStats(hub.ID, now)
	require.NoError(t, err)

	byDay := map[string]apps.CashHubDailyPoint{}
	for _, p := range stats.Daily {
		byDay[p.Date.Format("2006-01-02")] = p
	}
	point := byDay[day.Format("2006-01-02")]
	assert.EqualValues(t, 3900, point.IssuedMloki, "every event on a day must be summed")
	assert.EqualValues(t, 3900, point.RedeemedMloki, "and likewise for the redeemed series")
}

// TestGetCashHubStats_DailySeriesReturnedAndWrittenOff covers the two
// archive-only branches, which are keyed on archived_at rather than on a
// recipient's action — the buckets a past bug left structurally empty.
func TestGetCashHubStats_DailySeriesReturnedAndWrittenOff(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)

	now := time.Now().UTC()
	archivedAt := now.AddDate(0, 0, -1)

	require.NoError(t, svc.DB.Create(&db.CashBillSliceArchive{
		WalletAppID: 730_001, HubAppID: hub.ID, ClaimID: 31,
		IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(),
		AmountMloki: 700, Outcome: db.CashSliceStatusExpired,
		CreatedAt: now.AddDate(0, 0, -5), ArchivedAt: archivedAt,
	}).Error)
	require.NoError(t, svc.DB.Create(&db.CashBillSliceArchive{
		WalletAppID: 730_002, HubAppID: hub.ID, ClaimID: 32,
		IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(),
		AmountMloki: 300, Outcome: db.CashSliceStatusWrittenOff,
		CreatedAt: now.AddDate(0, 0, -5), ArchivedAt: archivedAt,
	}).Error)

	stats, err := svc.AppsService.GetCashHubStats(hub.ID, now)
	require.NoError(t, err)

	byDay := map[string]apps.CashHubDailyPoint{}
	for _, p := range stats.Daily {
		byDay[p.Date.Format("2006-01-02")] = p
	}
	point := byDay[archivedAt.Format("2006-01-02")]
	assert.EqualValues(t, 700, point.ReturnedMloki, "returned is keyed on when the bill was archived")
	assert.EqualValues(t, 300, point.WrittenOffMloki, "as is written-off")
}
