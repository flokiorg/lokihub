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

// seedMergedHub builds a hub with two live slices and three archived ones,
// interleaved in time so ordering across the live/archived boundary is
// actually exercised rather than accidentally satisfied.
//
// createdAt runs oldest to newest: archived(-5m), live(-4m), archived(-3m),
// live(-2m), archived(-1m, void).
func seedMergedHub(t *testing.T, svc *tests.TestService) (*db.App, *db.App) {
	t.Helper()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)

	bill, _, err := svc.AppsService.CreateApp(
		"cash-bill", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)

	now := time.Now()
	claimed := now.Add(-3 * time.Minute)

	// Two live slices: one unclaimed, one redeemed.
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(bill.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 2000, ClaimedAt: &claimed},
	}))
	var live []db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", bill.ID).Order("id").Find(&live).Error)
	require.Len(t, live, 2)
	require.NoError(t, svc.DB.Model(&live[0]).Update("created_at", now.Add(-4*time.Minute)).Error)
	require.NoError(t, svc.DB.Model(&live[1]).Update("created_at", now.Add(-2*time.Minute)).Error)

	// Three archived slices from a bill that no longer exists — including a
	// void one, which must stay out of the listing.
	for _, a := range []db.CashBillSliceArchive{
		{AmountMloki: 3000, Outcome: db.CashSliceStatusExpired, CreatedAt: now.Add(-5 * time.Minute)},
		{AmountMloki: 4000, Outcome: db.CashSliceStatusSplit, CreatedAt: now.Add(-3 * time.Minute)},
		{AmountMloki: 5000, Outcome: db.CashSliceStatusVoid, CreatedAt: now.Add(-1 * time.Minute)},
	} {
		a.WalletAppID = 999_000 + uint(a.AmountMloki)
		a.HubAppID = hub.ID
		a.ClaimID = 1
		a.IdentityType = db.CashIdentityPubkey
		a.IdentityValue = randomHex32()
		a.ArchivedAt = now
		require.NoError(t, svc.DB.Create(&a).Error)
	}
	return hub, bill
}

func TestListCashWalletClaims_MergesLiveAndArchived(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub, _ := seedMergedHub(t, svc)

	rows, total, counts, err := svc.AppsService.ListCashWalletClaims(apps.CashClaimFilter{
		HubID: hub.ID, Now: time.Now(),
	})
	require.NoError(t, err)

	// Four rows, newest first — the void one excluded.
	require.Len(t, rows, 4)
	assert.EqualValues(t, 4, total)
	assert.Equal(t, []int64{2000, 4000, 1000, 3000}, []int64{
		rows[0].AmountMloki, rows[1].AmountMloki, rows[2].AmountMloki, rows[3].AmountMloki,
	}, "ordering must interleave live and archived strictly by created_at, newest first")
	assert.Equal(t, []bool{false, true, false, true}, []bool{
		rows[0].Archived, rows[1].Archived, rows[2].Archived, rows[3].Archived,
	})

	assert.EqualValues(t, 1, counts[db.CashSliceStatusUnclaimed])
	assert.EqualValues(t, 1, counts[db.CashSliceStatusRedeemed])
	assert.EqualValues(t, 1, counts[db.CashSliceStatusSplit])
	assert.EqualValues(t, 1, counts[db.CashSliceStatusExpired])
	assert.EqualValues(t, 2, counts["claimed"], "the legacy umbrella is redeemed + split")
	assert.Zero(t, counts[db.CashSliceStatusVoid], "a void bill was never observable to anyone")
}

// TestListCashWalletClaims_PaginatesAcrossTheBoundary is the case a hand-rolled
// merge gets wrong: a page whose rows come from both sources.
func TestListCashWalletClaims_PaginatesAcrossTheBoundary(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub, _ := seedMergedHub(t, svc)
	now := time.Now()

	var seen []int64
	for offset := uint64(0); offset < 4; offset += 2 {
		page, total, _, err := svc.AppsService.ListCashWalletClaims(apps.CashClaimFilter{
			HubID: hub.ID, Limit: 2, Offset: offset, Now: now,
		})
		require.NoError(t, err)
		assert.EqualValues(t, 4, total, "total must describe the whole match, not the page")
		require.Len(t, page, 2)
		for _, r := range page {
			seen = append(seen, r.AmountMloki)
		}
	}
	assert.Equal(t, []int64{2000, 4000, 1000, 3000}, seen,
		"paging must not drop or repeat a row at the live/archived seam")
}

func TestListCashWalletClaims_StatusFilter(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub, _ := seedMergedHub(t, svc)
	now := time.Now()

	for _, tc := range []struct {
		status string
		want   []int64
	}{
		{db.CashSliceStatusUnclaimed, []int64{1000}},
		{db.CashSliceStatusRedeemed, []int64{2000}},
		{db.CashSliceStatusSplit, []int64{4000}},
		{db.CashSliceStatusExpired, []int64{3000}},
		// The legacy value still works, and still means "not unclaimed".
		{"claimed", []int64{2000, 4000}},
	} {
		t.Run(tc.status, func(t *testing.T) {
			rows, total, counts, err := svc.AppsService.ListCashWalletClaims(apps.CashClaimFilter{
				HubID: hub.ID, Status: tc.status, Now: now,
			})
			require.NoError(t, err)
			got := make([]int64, 0, len(rows))
			for _, r := range rows {
				got = append(got, r.AmountMloki)
			}
			assert.Equal(t, tc.want, got)
			assert.EqualValues(t, len(tc.want), total)
			assert.EqualValues(t, 1, counts[db.CashSliceStatusUnclaimed],
				"counts must describe the unfiltered set, so a UI's facet counts do not move when a facet is picked")
		})
	}
}

// TestListCashWalletClaims_UnknownStatusIsRejected: an unrecognised status used
// to return an empty page, which is indistinguishable from a hub that simply
// has no such slices — a client typo looked like real data.
func TestListCashWalletClaims_UnknownStatusIsRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub, _ := seedMergedHub(t, svc)
	_, _, _, err = svc.AppsService.ListCashWalletClaims(apps.CashClaimFilter{
		HubID: hub.ID, Status: "not-a-status", Now: time.Now(),
	})
	require.ErrorIs(t, err, constants.ErrInvalidParams)
}

// TestListCashWalletClaims_ExpiryIsDerivedFromTheCallersClock: a live unclaimed
// slice reads as expired once its bill's deadline passes, without anything
// being written.
func TestListCashWalletClaims_ExpiryIsDerivedFromTheCallersClock(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	deadline := time.Now().Add(time.Hour)
	bill, _, err := svc.AppsService.CreateApp(
		"cash-bill", "", 0, constants.BUDGET_RENEWAL_NEVER, &deadline,
		[]string{constants.CASH_REDEEM_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(bill.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 1000},
	}))

	rows, _, _, err := svc.AppsService.ListCashWalletClaims(apps.CashClaimFilter{HubID: hub.ID, Now: time.Now()})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, db.CashSliceStatusUnclaimed, rows[0].Status)

	rows, _, _, err = svc.AppsService.ListCashWalletClaims(apps.CashClaimFilter{
		HubID: hub.ID, Now: deadline.Add(time.Second),
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, db.CashSliceStatusExpired, rows[0].Status)
}
