package apps_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// newStatsBill mints a cash bill on hub with the given slices, pushing the
// mint time of every settled slice back to mintedAt.
//
// gorm stamps created_at on insert, so a slice would otherwise appear to have
// been minted after it settled — which makes its mint-to-settle span negative
// and drops it from the median entirely.
func newStatsBill(t *testing.T, svc *tests.TestService, hubID uint, mintedAt time.Time, claims []db.CashWalletClaim) *db.App {
	t.Helper()
	bill, _, err := svc.AppsService.CreateApp(
		"cash-bill", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE}, db.AppKindCashWallet, &hubID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(bill.ID, claims))
	require.NoError(t, svc.DB.Model(&db.CashWalletClaim{}).
		Where("wallet_app_id = ? AND settled_at IS NOT NULL", bill.ID).
		Update("created_at", mintedAt).Error)
	return bill
}

// The Cash Hubs list shows one set of figures for every hub at once. Those
// figures have to be the same money the individual dashboards show, or an
// operator reconciling the two finds a discrepancy that does not exist.
//
// Summing per-hub results in Go would pass this test and still be the wrong
// implementation — it would issue a query per hub, and could not produce a
// correct median, since the median of several medians is not the median of the
// whole. So the node-wide scope runs the same queries with the hub predicates
// removed, and this pins the arithmetic that follows from that.
func TestGetAllCashHubStats_MatchesTheSumOfEachHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	now := time.Now()
	claimed := now.Add(-2 * time.Hour)
	settled := claimed.Add(time.Minute)
	// Every redeemed slice is minted exactly a minute before it settles, so
	// the median below is a known value rather than an artefact of timing.
	minted := settled.Add(-time.Minute)

	hubA := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	newStatsBill(t, svc, hubA.ID, minted, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 2000,
			ClaimedAt: &claimed, SettledAt: &settled, RedeemFeeMloki: 20, PaymentHash: randomHex32()},
	})

	hubB := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	newStatsBill(t, svc, hubB.ID, minted, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 4000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 8000,
			ClaimedAt: &claimed, SettledAt: &settled, RedeemFeeMloki: 80, PaymentHash: randomHex32()},
	})

	statsA, err := svc.AppsService.GetCashHubStats(hubA.ID, now)
	require.NoError(t, err)
	statsB, err := svc.AppsService.GetCashHubStats(hubB.ID, now)
	require.NoError(t, err)

	all, err := svc.AppsService.GetAllCashHubStats(now)
	require.NoError(t, err)

	assert.Equal(t, statsA.OutstandingMloki+statsB.OutstandingMloki, all.OutstandingMloki)
	assert.Equal(t, statsA.OutstandingCount+statsB.OutstandingCount, all.OutstandingCount)
	assert.Equal(t, statsA.IssuedMloki+statsB.IssuedMloki, all.IssuedMloki)
	assert.Equal(t, statsA.IssuedCount+statsB.IssuedCount, all.IssuedCount)
	assert.Equal(t, statsA.RedeemedMloki+statsB.RedeemedMloki, all.RedeemedMloki)
	assert.Equal(t, statsA.RedeemedCount+statsB.RedeemedCount, all.RedeemedCount)
	assert.Equal(t, statsA.FeesEarnedMloki+statsB.FeesEarnedMloki, all.FeesEarnedMloki)

	// Concrete values too, so the test still means something if both sides
	// were to break in the same direction.
	assert.EqualValues(t, 5000, all.OutstandingMloki, "1000 + 4000 still redeemable")
	assert.EqualValues(t, 15000, all.IssuedMloki, "every slice on both hubs")
	assert.EqualValues(t, 10000, all.RedeemedMloki, "2000 + 8000 paid out")
	assert.EqualValues(t, 100, all.FeesEarnedMloki, "20 + 80 charged")

	// The daily series must cover the same window and carry both hubs.
	require.NotEmpty(t, all.Daily)
	require.Len(t, all.Daily, len(statsA.Daily), "the node-wide window must match a single hub's")
	var issued int64
	for _, d := range all.Daily {
		issued += d.IssuedMloki
	}
	assert.EqualValues(t, 15000, issued, "the series must sum back to the total, across both hubs")

	require.NotNil(t, statsA.MedianTimeToRedeemSecs, "the per-hub median must exist for the node-wide one to mean anything")
	require.NotNil(t, all.MedianTimeToRedeemSecs)
	assert.EqualValues(t, 60, *all.MedianTimeToRedeemSecs,
		"every redemption took a minute, so the node-wide median is a minute")
}

// A node with no cash hubs must return zeroes and a full window of empty days,
// not an error and not a nil series — the list page renders this before the
// operator has created anything.
func TestGetAllCashHubStats_EmptyNode(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	all, err := svc.AppsService.GetAllCashHubStats(time.Now())
	require.NoError(t, err)
	assert.Zero(t, all.OutstandingMloki)
	assert.Zero(t, all.IssuedMloki)
	assert.Nil(t, all.MedianTimeToRedeemSecs, "nothing has been redeemed, so there is no median")
	assert.NotEmpty(t, all.Daily, "the chart needs its days even when every one of them is empty")
}

// The node-wide SQL is derived from the per-hub string by removing its two hub
// predicates. A rename on either side would silently stop the replacement
// matching, leaving a query with placeholders nothing supplies — so assert the
// derivation actually happened rather than trusting it.
func TestAllHubsCashClaimUnionSQL_IsDerivedNotStale(t *testing.T) {
	perHub := apps.ExportedCashClaimUnionSQL
	allHubs := apps.ExportedAllHubsCashClaimUnionSQL

	require.NotEqual(t, perHub, allHubs, "the node-wide variant must differ from the per-hub one")
	assert.NotContains(t, allHubs, "a.parent_app_id = ?",
		"the live branch's hub predicate must be gone, or the query takes a placeholder no caller supplies")
	assert.NotContains(t, allHubs, "s.hub_app_id = ?",
		"the archived branch's hub predicate must be gone, for the same reason")

	// The rest of the query must survive intact: both branches, and the
	// filters that are not about scoping.
	assert.Equal(t, 1, strings.Count(allHubs, "UNION ALL"),
		"both branches must still be present")
	assert.Contains(t, allHubs, "a.parent_kind = 'cash' AND a.kind = 'cash_wallet'")
	assert.Contains(t, allHubs, "s.outcome <> 'void'")
	assert.Equal(t, strings.Count(perHub, "?")-2, strings.Count(allHubs, "?"),
		"exactly the two hub placeholders may be removed, no others")
}

// The expiry buckets must partition the outstanding total exactly. They are
// rendered directly beneath it, so a bucket set that summed to anything else
// would read as money appearing or vanishing between two figures on one
// screen.
func TestGetAllCashHubStats_ExpiryBucketsPartitionOutstanding(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	now := time.Now()
	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)

	// One bill per bucket, plus one that never expires.
	for _, tc := range []struct {
		expiresIn time.Duration
		never     bool
		amount    int64
		want      string
	}{
		{expiresIn: 6 * time.Hour, amount: 1000, want: "24h"},
		{expiresIn: 3 * 24 * time.Hour, amount: 2000, want: "7d"},
		{expiresIn: 14 * 24 * time.Hour, amount: 4000, want: "30d"},
		{expiresIn: 90 * 24 * time.Hour, amount: 8000, want: "later"},
		{never: true, amount: 16000, want: "never"},
	} {
		bill := newStatsBill(t, svc, hub.ID, now, []db.CashWalletClaim{
			{IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: tc.amount},
		})
		if tc.never {
			require.NoError(t, svc.DB.Model(&db.App{}).
				Where("id = ?", bill.ID).Update("expires_at", nil).Error)
			continue
		}
		require.NoError(t, svc.DB.Model(&db.App{}).
			Where("id = ?", bill.ID).Update("expires_at", now.Add(tc.expiresIn)).Error)
	}

	stats, err := svc.AppsService.GetAllCashHubStats(now)
	require.NoError(t, err)

	byKey := map[string]int64{}
	var summed int64
	for _, b := range stats.ExpiryBuckets {
		byKey[b.Key] = b.Mloki
		summed += b.Mloki
	}
	assert.Equal(t, stats.OutstandingMloki, summed,
		"the buckets must sum back to the outstanding total they sit under")

	assert.EqualValues(t, 1000, byKey["24h"])
	assert.EqualValues(t, 2000, byKey["7d"])
	assert.EqualValues(t, 4000, byKey["30d"])
	assert.EqualValues(t, 8000, byKey["later"])
	assert.EqualValues(t, 16000, byKey["never"],
		"a bill with no deadline must land in never, not in the furthest dated bucket")

	// Order is part of the contract: the chart renders them as given.
	require.Len(t, stats.ExpiryBuckets, 5)
	assert.Equal(t, []string{"24h", "7d", "30d", "later", "never"},
		[]string{
			stats.ExpiryBuckets[0].Key, stats.ExpiryBuckets[1].Key,
			stats.ExpiryBuckets[2].Key, stats.ExpiryBuckets[3].Key,
			stats.ExpiryBuckets[4].Key,
		})
}

// The per-hub split has to include hubs owing nothing. The chart answers
// "which hub is carrying this", and a hub that drops out of the answer
// because it has settled looks deleted rather than clean.
func TestGetAllCashHubStats_PerHubIncludesHubsOwingNothing(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	now := time.Now()
	owing := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	idle := tests.CreateCashHub(t, svc, 1_000_000, 3600)

	newStatsBill(t, svc, owing.ID, now, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 5000},
	})

	stats, err := svc.AppsService.GetAllCashHubStats(now)
	require.NoError(t, err)
	require.Len(t, stats.PerHub, 2, "both hubs must appear, not just the one owing something")

	byID := map[uint]int64{}
	for _, h := range stats.PerHub {
		byID[h.HubAppID] = h.OutstandingMloki
	}
	assert.EqualValues(t, 5000, byID[owing.ID])
	assert.EqualValues(t, 0, byID[idle.ID])

	// Ordered largest-first, so the hub that needs attention is the first bar.
	assert.Equal(t, owing.ID, stats.PerHub[0].HubAppID)

	var summed int64
	for _, h := range stats.PerHub {
		summed += h.OutstandingMloki
	}
	assert.Equal(t, stats.OutstandingMloki, summed,
		"the per-hub split must sum back to the node's own outstanding total")
}
