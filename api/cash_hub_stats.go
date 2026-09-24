package api

import (
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
)

// GetCashHubStats totals one cash_hub's activity for its dashboard.
//
// The figures span live and archived slices alike, which is what the archive
// bought: before it, a bill's history was destroyed the moment it was spent, so
// nothing could report what a hub had actually issued or paid out.
func (api *api) GetCashHubStats(appID uint) (*CashHubStatsResponse, error) {
	var app db.App
	if err := api.db.First(&app, appID).Error; err != nil {
		return nil, fmt.Errorf("app not found: %w", err)
	}
	if app.Kind != db.AppKindCashHub {
		return nil, fmt.Errorf("app is not a cash_hub")
	}

	stats, err := api.appsSvc.GetCashHubStats(appID, time.Now())
	if err != nil {
		return nil, err
	}
	resp := cashHubStatsResponse(stats)
	if err := api.fillCashBacking(resp, &appID); err != nil {
		return nil, err
	}
	return resp, nil
}

// fillCashBacking computes what the issued bills actually hold, and whether
// any individual bill is underfunded. hubID nil means every cash hub.
//
// Per bill, deliberately. The whole point is that an aggregate cannot answer
// it: a hub sitting on a large unminted balance would hide one bill whose own
// ledger had fallen below its unclaimed slices, which is the exact failure a
// solvency reading exists to catch. Only deficits are summed, so a bill
// holding more than it owes never offsets one holding less.
func (api *api) fillCashBacking(resp *CashHubStatsResponse, hubID *uint) error {
	type billRow struct {
		AppID          uint
		UnclaimedMloki int64
	}
	var bills []billRow
	q := api.db.Table("apps a").
		Select("a.id AS app_id, COALESCE(SUM(CASE WHEN c.claimed_at IS NULL THEN c.amount_mloki ELSE 0 END), 0) AS unclaimed_mloki").
		Joins("LEFT JOIN cash_wallet_claims c ON c.wallet_app_id = a.id").
		Where("a.parent_kind = ? AND a.kind = ?", db.ParentKindCash, db.AppKindCashWallet).
		Group("a.id")
	if hubID != nil {
		q = q.Where("a.parent_app_id = ?", *hubID)
	}
	if err := q.Scan(&bills).Error; err != nil {
		return fmt.Errorf("failed to list live cash bills: %w", err)
	}
	if len(bills) == 0 {
		return nil
	}

	ids := make([]uint, 0, len(bills))
	for _, b := range bills {
		ids = append(ids, b.AppID)
	}
	balances, err := queries.GetIsolatedBalancesByAppIDs(api.db, ids)
	if err != nil {
		return fmt.Errorf("failed to total cash bill balances: %w", err)
	}
	for _, b := range bills {
		balance := balances[b.AppID]
		resp.BackingMloki += balance
		if short := b.UnclaimedMloki - balance; short > 0 {
			resp.ShortfallMloki += short
		}
	}
	return nil
}

// GetAllCashHubStats totals every cash_hub on the node, for the Cash Hubs
// list's own overview — the same figures with the same meanings as one hub's
// dashboard, so the list and the dashboards reconcile.
//
// There is no app to look up and therefore no kind to check: the scope is
// "every cash hub", including none at all, which is a valid answer rather than
// an error.
func (api *api) GetAllCashHubStats() (*CashHubStatsResponse, error) {
	stats, err := api.appsSvc.GetAllCashHubStats(time.Now())
	if err != nil {
		return nil, err
	}
	resp := cashHubStatsResponse(stats)
	if err := api.fillCashBacking(resp, nil); err != nil {
		return nil, err
	}

	// The hub count and their combined balance are filled here rather than in
	// the apps service, because the canonical balance arithmetic lives in
	// db/queries and apps cannot import it: db/queries' own tests import the
	// tests helper, which imports apps, so the edge would close a cycle.
	//
	// Doing it here is also where it belongs. This is the one figure the list
	// cannot work out for itself — it filters its page of apps client-side, so
	// anything it counted would be the current page's rather than the node's —
	// and reusing GetIsolatedBalancesByAppIDs keeps it the same arithmetic,
	// fee reserves and skims included, as every other balance on the node.
	var hubIDs []uint
	if err := api.db.Model(&db.App{}).
		Where("kind = ?", db.AppKindCashHub).
		Pluck("id", &hubIDs).Error; err != nil {
		return nil, fmt.Errorf("failed to list cash hubs: %w", err)
	}
	resp.HubsCount = uint64(len(hubIDs))

	balances, err := queries.GetIsolatedBalancesByAppIDs(api.db, hubIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to total cash hub balances: %w", err)
	}
	for _, b := range balances {
		resp.BalanceMloki += b
	}

	// The per-hub rows get their balance from the same map, so a hub's own
	// coverage and the node's total coverage can never disagree.
	resp.PerHub = make([]CashHubOutstandingResponse, 0, len(stats.PerHub))
	for _, h := range stats.PerHub {
		resp.PerHub = append(resp.PerHub, CashHubOutstandingResponse{
			HubAppID:         h.HubAppID,
			Name:             h.Name,
			OutstandingMloki: h.OutstandingMloki,
			OutstandingCount: h.OutstandingCount,
			BalanceMloki:     balances[h.HubAppID],
		})
	}
	return resp, nil
}

func cashHubStatsResponse(stats *apps.CashHubStats) *CashHubStatsResponse {
	buckets := make([]CashExpiryBucketResponse, 0, len(stats.ExpiryBuckets))
	for _, b := range stats.ExpiryBuckets {
		buckets = append(buckets, CashExpiryBucketResponse{
			Key: b.Key, Mloki: b.Mloki, Count: b.Count,
		})
	}

	daily := make([]CashHubDailyPointResponse, 0, len(stats.Daily))
	for _, p := range stats.Daily {
		daily = append(daily, CashHubDailyPointResponse{
			Date:          p.Date.Format("2006-01-02"),
			IssuedMloki:   p.IssuedMloki,
			RedeemedMloki: p.RedeemedMloki,
			ReturnedMloki: p.ReturnedMloki,

			SplitMloki:      p.SplitMloki,
			WrittenOffMloki: p.WrittenOffMloki,
		})
	}

	return &CashHubStatsResponse{
		OutstandingMloki:       stats.OutstandingMloki,
		OutstandingCount:       stats.OutstandingCount,
		IssuedMloki:            stats.IssuedMloki,
		IssuedCount:            stats.IssuedCount,
		RedeemedMloki:          stats.RedeemedMloki,
		RedeemedCount:          stats.RedeemedCount,
		SplitMloki:             stats.SplitMloki,
		SplitCount:             stats.SplitCount,
		ReturnedMloki:          stats.ReturnedMloki,
		ReturnedCount:          stats.ReturnedCount,
		WrittenOffMloki:        stats.WrittenOffMloki,
		WrittenOffCount:        stats.WrittenOffCount,
		FeesEarnedMloki:        stats.FeesEarnedMloki,
		MedianTimeToRedeemSecs: stats.MedianTimeToRedeemSecs,
		Daily:                  daily,
		ExpiryBuckets:          buckets,
	}
}
