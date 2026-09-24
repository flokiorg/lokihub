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
	return cashHubStatsResponse(stats), nil
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
