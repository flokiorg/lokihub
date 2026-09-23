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
		a.WalletAppID = 500_000 + uint(a.AmountMloki)
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
