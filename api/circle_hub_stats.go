package api

import (
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
)

// GetCircleHubStats totals one circle_hub's activity for its dashboard.
//
// A circle hub had no stats at all before this: its page showed a table of
// member wallets and the generic AppUsage card, which walks every transaction
// the hub ever had, a page at a time, to render two numbers. Nothing said what
// had been allocated, who was spending it, or whether budgets were filling up.
func (api *api) GetCircleHubStats(appID uint) (*CircleHubStatsResponse, error) {
	var app db.App
	if err := api.db.First(&app, appID).Error; err != nil {
		return nil, fmt.Errorf("app not found: %w", err)
	}
	if app.Kind != db.AppKindCircleHub {
		return nil, fmt.Errorf("app is not a circle_hub")
	}

	stats, err := api.appsSvc.GetCircleHubStats(appID, time.Now())
	if err != nil {
		return nil, err
	}

	// Balances come from the canonical helper, as the cash totals do, so a
	// member's balance here and on the wallets list can never disagree.
	memberIDs := make([]uint, 0, len(stats.PerMember))
	for _, m := range stats.PerMember {
		memberIDs = append(memberIDs, m.WalletAppID)
	}
	balances, err := queries.GetIsolatedBalancesByAppIDs(api.db, memberIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to total circle member balances: %w", err)
	}

	members := make([]CircleMemberActivityResponse, 0, len(stats.PerMember))
	var allocated int64
	for _, m := range stats.PerMember {
		balance := balances[m.WalletAppID]
		allocated += balance
		members = append(members, CircleMemberActivityResponse{
			WalletAppID:    m.WalletAppID,
			Name:           m.Name,
			SpentMloki:     m.SpentMloki,
			BalanceMloki:   balance,
			MaxAmountMloki: m.MaxAmountMloki,
		})
	}

	daily := make([]CircleHubDailyPointResponse, 0, len(stats.Daily))
	for _, p := range stats.Daily {
		daily = append(daily, CircleHubDailyPointResponse{
			Date:          p.Date.Format("2006-01-02"),
			SpentMloki:    p.SpentMloki,
			ReceivedMloki: p.ReceivedMloki,
		})
	}

	return &CircleHubStatsResponse{
		MembersCount:    stats.MembersCount,
		EligibleCount:   stats.EligibleCount,
		AllocatedMloki:  allocated,
		SpentMloki:      stats.SpentMloki,
		SpentCount:      stats.SpentCount,
		ReceivedMloki:   stats.ReceivedMloki,
		ReceivedCount:   stats.ReceivedCount,
		FeesEarnedMloki: stats.FeesEarnedMloki,
		Daily:           daily,
		PerMember:       members,
	}, nil
}
