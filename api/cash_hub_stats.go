package api

import (
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/db"
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
	}, nil
}
