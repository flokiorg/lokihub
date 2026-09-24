package apps

import (
	"fmt"
	"sort"
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// circleStatsWindowDays matches the cash window, so the two hub kinds' charts
// cover the same span and can be read against each other.
const circleStatsWindowDays = cashStatsWindowDays

// CircleHubStats is what a circle host actually watches.
//
// A circle hub's money question is the mirror image of a cash hub's. A cash
// hub owes value it has already handed out as bearer bills; a circle hub has
// *allocated* value into wallets its members still hold, and can reclaim. So
// the headline is not a liability but an allocation, and the risk is not
// insolvency but drift: budgets quietly filling up, or one member spending
// far more than the rest.
type CircleHubStats struct {
	// MembersCount is how many circle_wallet children exist. EligibleCount is
	// how many pubkeys may request one, and is nil for a "following"-policy
	// circle, whose eligible set is the host's live contact list rather than
	// anything this hub stores.
	MembersCount  uint64
	EligibleCount *uint64

	// AllocatedMloki is what members are currently holding, filled by the api
	// layer which owns the canonical balance arithmetic.
	AllocatedMloki int64

	// SpentMloki/ReceivedMloki are settled payments made and received by
	// member wallets. Spent excludes the hub's own fee cut, which is reported
	// separately as FeesEarnedMloki rather than buried in what members spent.
	SpentMloki    int64
	SpentCount    uint64
	ReceivedMloki int64
	ReceivedCount uint64

	// FeesEarnedMloki is the hub's forwarding cut on member payments
	// (db.Transaction.FeeSkimMloki).
	FeesEarnedMloki int64

	Daily     []CircleHubDailyPoint
	PerMember []CircleMemberActivity
}

// CircleHubDailyPoint is one day of member activity, oldest first. Every day
// in the window is present even when nothing happened, so a chart has no gaps.
type CircleHubDailyPoint struct {
	Date          time.Time
	SpentMloki    int64
	ReceivedMloki int64
}

// CircleMemberActivity is one member's share. Every member appears, including
// those who have never spent: on a breakdown the question is "who is using
// this", and a member who drops out of the answer looks removed rather than
// idle.
type CircleMemberActivity struct {
	WalletAppID uint
	Name        string
	SpentMloki  int64
	// BalanceMloki and MaxAmountMloki are filled by the api layer.
	BalanceMloki   int64
	MaxAmountMloki int64
}

// GetCircleHubStats totals one circle_hub's activity for its dashboard.
func (svc *appsService) GetCircleHubStats(hubID uint, now time.Time) (*CircleHubStats, error) {
	stats := &CircleHubStats{}

	// A member's spend cap lives on its pay_invoice permission, not on the App
	// row — budgets are per scope here — so it has to be joined rather than
	// selected alongside the name. LEFT JOIN because a member without that
	// permission has no cap rather than a cap of zero, and the two must not
	// collapse into the same number.
	var members []struct {
		ID            uint
		Name          string
		MaxAmountLoki int64
	}
	if err := svc.db.Raw(`
		SELECT a.id AS id, a.name AS name,
		       COALESCE(p.max_amount_loki, 0) AS max_amount_loki
		FROM apps a
		LEFT JOIN app_permissions p ON p.app_id = a.id AND p.scope = ?
		WHERE a.parent_app_id = ? AND a.parent_kind = ? AND a.kind = ?`,
		constants.PAY_INVOICE_SCOPE, hubID, db.ParentKindCircle, db.AppKindCircleWallet).
		Scan(&members).Error; err != nil {
		return nil, fmt.Errorf("failed to list circle members for hub %d: %w", hubID, err)
	}
	stats.MembersCount = uint64(len(members))

	if err := svc.fillCircleEligibleCount(stats, hubID); err != nil {
		return nil, err
	}

	// Settled flows across every member wallet. A pending outgoing payment is
	// deliberately excluded: it has not left yet, and counting it would make
	// "spent" jump and fall back if it fails.
	type flowRow struct {
		WalletAppID uint
		Type        string
		AmountMloki int64
		SkimMloki   int64
		N           uint64
	}
	var flows []flowRow
	if err := svc.db.Raw(`
		SELECT t.app_id AS wallet_app_id, t.type AS type,
		       COALESCE(SUM(t.amount_mloki), 0) AS amount_mloki,
		       COALESCE(SUM(t.fee_skim_mloki), 0) AS skim_mloki,
		       COUNT(*) AS n
		FROM transactions t
		JOIN apps a ON a.id = t.app_id
		WHERE a.parent_app_id = ? AND a.parent_kind = ? AND a.kind = ?
		  AND t.state = ?
		GROUP BY t.app_id, t.type`,
		hubID, db.ParentKindCircle, db.AppKindCircleWallet,
		constants.TRANSACTION_STATE_SETTLED).Scan(&flows).Error; err != nil {
		return nil, fmt.Errorf("failed to total circle flows for hub %d: %w", hubID, err)
	}

	spentByMember := map[uint]int64{}
	for _, f := range flows {
		switch f.Type {
		case constants.TRANSACTION_TYPE_OUTGOING:
			stats.SpentMloki += f.AmountMloki
			stats.SpentCount += f.N
			stats.FeesEarnedMloki += f.SkimMloki
			spentByMember[f.WalletAppID] += f.AmountMloki
		case constants.TRANSACTION_TYPE_INCOMING:
			stats.ReceivedMloki += f.AmountMloki
			stats.ReceivedCount += f.N
		}
	}

	stats.PerMember = make([]CircleMemberActivity, 0, len(members))
	for _, m := range members {
		stats.PerMember = append(stats.PerMember, CircleMemberActivity{
			WalletAppID: m.ID,
			Name:        m.Name,
			SpentMloki:  spentByMember[m.ID],
			// The App row stores its cap in loki, unlike every amount
			// around it — scaled once here so only one unit reaches the UI.
			MaxAmountMloki: m.MaxAmountLoki * 1000,
		})
	}
	sort.Slice(stats.PerMember, func(i, j int) bool {
		if stats.PerMember[i].SpentMloki != stats.PerMember[j].SpentMloki {
			return stats.PerMember[i].SpentMloki > stats.PerMember[j].SpentMloki
		}
		return stats.PerMember[i].WalletAppID < stats.PerMember[j].WalletAppID
	})

	since := now.AddDate(0, 0, -circleStatsWindowDays).Truncate(24 * time.Hour)
	if err := svc.fillCircleDailySeries(stats, hubID, since, now); err != nil {
		return nil, err
	}
	return stats, nil
}

// fillCircleEligibleCount reports how many pubkeys may request a wallet.
//
// Only an allowlist circle has an answer this hub can give. A "following"
// circle draws its members from the host's live kind:3 contact list, which is
// not stored here and can change without this hub being told — so the honest
// value is "unknown" rather than a stale number presented as fact.
func (svc *appsService) fillCircleEligibleCount(stats *CircleHubStats, hubID uint) error {
	var config db.CircleHubConfig
	if err := svc.db.Preload("CircleIdentity").
		Where("app_id = ?", hubID).First(&config).Error; err != nil {
		// A circle hub with no config row is malformed, not a reason to fail
		// the whole dashboard.
		return nil
	}
	if config.CircleIdentity.Policy != db.CirclePolicyAllowlist {
		return nil
	}

	var n int64
	if err := svc.db.Model(&db.CircleIdentityAllowedPubkey{}).
		Where("circle_identity_id = ?", config.CircleIdentityID).
		Count(&n).Error; err != nil {
		return fmt.Errorf("failed to count allowlist for hub %d: %w", hubID, err)
	}
	count := uint64(n)
	stats.EligibleCount = &count
	return nil
}

// fillCircleDailySeries buckets member spending and receipts into days.
//
// Keyed on settled_at rather than created_at: the question is when the money
// actually moved, not when the row happened to be written. Bucketing happens
// in Go for the same reason the cash series does it — the boundaries are
// relative to now, and expressing that in SQL means date arithmetic that
// differs between sqlite and Postgres.
func (svc *appsService) fillCircleDailySeries(stats *CircleHubStats, hubID uint, since, now time.Time) error {
	type event struct {
		At          time.Time
		Type        string
		AmountMloki int64
	}
	var events []event
	if err := svc.db.Raw(`
		SELECT t.settled_at AS at, t.type AS type, t.amount_mloki AS amount_mloki
		FROM transactions t
		JOIN apps a ON a.id = t.app_id
		WHERE a.parent_app_id = ? AND a.parent_kind = ? AND a.kind = ?
		  AND t.state = ? AND t.settled_at IS NOT NULL AND t.settled_at >= ?`,
		hubID, db.ParentKindCircle, db.AppKindCircleWallet,
		constants.TRANSACTION_STATE_SETTLED, since).Scan(&events).Error; err != nil {
		return fmt.Errorf("failed to read circle daily series for hub %d: %w", hubID, err)
	}

	buckets := map[time.Time]*CircleHubDailyPoint{}
	for d := since; !d.After(now); d = d.AddDate(0, 0, 1) {
		day := d.UTC().Truncate(24 * time.Hour)
		buckets[day] = &CircleHubDailyPoint{Date: day}
	}
	for _, e := range events {
		b, ok := buckets[e.At.UTC().Truncate(24*time.Hour)]
		if !ok {
			continue
		}
		switch e.Type {
		case constants.TRANSACTION_TYPE_OUTGOING:
			b.SpentMloki += e.AmountMloki
		case constants.TRANSACTION_TYPE_INCOMING:
			b.ReceivedMloki += e.AmountMloki
		}
	}

	stats.Daily = make([]CircleHubDailyPoint, 0, len(buckets))
	for _, b := range buckets {
		stats.Daily = append(stats.Daily, *b)
	}
	sort.Slice(stats.Daily, func(i, j int) bool {
		return stats.Daily[i].Date.Before(stats.Daily[j].Date)
	})
	return nil
}
