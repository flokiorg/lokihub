package apps

import (
	"fmt"
	"sort"
	"time"

	"github.com/flokiorg/lokihub/db"
)

// CashHubStats is what a hub operator actually watches: money, not row counts.
//
// Every figure spans a bill's whole life, live and archived alike — which is
// only possible because a spent bill's history survives its deletion. Before
// the archive there was nothing to total.
type CashHubStats struct {
	// OutstandingMloki is the hub's live liability: value in slices that can
	// still be redeemed. The headline figure, and the one nothing surfaced
	// before.
	OutstandingMloki int64
	OutstandingCount uint64

	IssuedMloki   int64
	IssuedCount   uint64
	RedeemedMloki int64
	RedeemedCount uint64
	// SplitMloki is value that moved into another bill rather than leaving —
	// churn, not outflow, so it is deliberately not counted as redeemed.
	SplitMloki int64
	SplitCount uint64
	// ReturnedMloki came back to the hub because a bill expired or was
	// deleted before anyone claimed it.
	ReturnedMloki int64
	ReturnedCount uint64
	// WrittenOffMloki could not be returned anywhere: the parent hub was gone.
	// Kept separate because it is a loss, not a recovery.
	WrittenOffMloki int64
	WrittenOffCount uint64

	// FeesEarnedMloki totals the hub's own redeem cut across every redeemed
	// slice.
	FeesEarnedMloki int64

	// MedianTimeToRedeemSecs says whether bills circulate or sit dead. nil
	// when nothing has been redeemed yet.
	MedianTimeToRedeemSecs *int64

	// Daily is a newest-last series for charting flow.
	Daily []CashHubDailyPoint
}

// CashHubDailyPoint is one day's flow. Date is midnight UTC.
type CashHubDailyPoint struct {
	Date          time.Time
	IssuedMloki   int64
	RedeemedMloki int64
	ReturnedMloki int64
	// SplitMloki and WrittenOffMloki are the two other ways a slice stops
	// being outstanding. Without them a client reconstructing the outstanding
	// curve from this series (issued less what left) overstates it by every
	// split and every write-off, and its right-hand end would not land on the
	// OutstandingMloki figure above. ReturnedMloki is correspondingly limited
	// to 'expired'/'reclaimed' here, matching what ReturnedMloki means in the
	// totals rather than quietly folding write-offs in.
	SplitMloki      int64
	WrittenOffMloki int64
}

// cashStatsWindowDays bounds the daily series and the median sample. A chart
// nobody scrolls past a month does not need more, and both queries stay
// proportional to the window rather than to a forever-growing archive.
const cashStatsWindowDays = 30

// medianSampleLimit bounds how many redeemed slices feed the median. A true
// median over the whole archive would mean reading every redeemed row this hub
// ever had, forever; the most recent thousand answers the question being asked
// ("are bills circulating lately?") without that cost.
const medianSampleLimit = 1000

// GetCashHubStats totals one hub's cash activity across live and archived
// slices.
//
// The sums are plain SQL. The daily series and the median are bucketed in Go
// instead, deliberately: date bucketing has no portable spelling across sqlite
// and Postgres, and the alternative — a dialect branch in a money query — is a
// worse thing to maintain than a loop over a bounded window.
func (svc *appsService) GetCashHubStats(hubID uint, now time.Time) (*CashHubStats, error) {
	stats := &CashHubStats{}

	// Live slices: only the unclaimed ones are still a liability. A claimed
	// live slice has already been counted by its terminal branch below.
	//
	// The expiry condition is load-bearing, not belt-and-braces. A slice whose
	// window has passed but which the sweep has not collected yet is still
	// live with claimed_at IS NULL, yet it is NOT redeemable — permissions.
	// HasPermission rejects it on the wallet's own ExpiresAt. Counting it here
	// would both contradict this figure's own meaning ("value still
	// redeemable") and double-count it, since the union below already reports
	// it as 'expired' and it is summed into ReturnedMloki. This condition
	// mirrors cashClaimUnionSQL's own 'expired' branch exactly, so every slice
	// lands in exactly one bucket and Issued == Outstanding + Redeemed +
	// Split + Returned + WrittenOff holds at all times.
	var liveOutstanding struct {
		Total int64
		N     uint64
	}
	if err := svc.db.Raw(`
		SELECT COALESCE(SUM(c.amount_mloki), 0) AS total, COUNT(*) AS n
		FROM cash_wallet_claims c
		JOIN apps a ON a.id = c.wallet_app_id
		WHERE a.parent_app_id = ? AND a.parent_kind = 'cash' AND a.kind = 'cash_wallet'
		  AND c.claimed_at IS NULL
		  AND (a.expires_at IS NULL OR a.expires_at >= ?)`, hubID, now).Scan(&liveOutstanding).Error; err != nil {
		return nil, fmt.Errorf("failed to total outstanding cash for hub %d: %w", hubID, err)
	}
	stats.OutstandingMloki = liveOutstanding.Total
	stats.OutstandingCount = liveOutstanding.N

	// Terminal outcomes, live and archived together. A live slice can be
	// terminal too (redeemed or split on a bill that is still alive because a
	// sibling slice is unclaimed), so both sources must be summed.
	type outcomeTotal struct {
		Status string
		Total  int64
		N      uint64
		Fees   int64
	}
	var totals []outcomeTotal
	if err := svc.db.Raw(`
		SELECT status, COALESCE(SUM(amount_mloki), 0) AS total, COUNT(*) AS n,
		       COALESCE(SUM(redeem_fee_mloki), 0) AS fees
		FROM (`+cashClaimUnionSQL+`) u
		GROUP BY status`, now, hubID, hubID).Scan(&totals).Error; err != nil {
		return nil, fmt.Errorf("failed to total cash outcomes for hub %d: %w", hubID, err)
	}
	for _, t := range totals {
		stats.IssuedMloki += t.Total
		stats.IssuedCount += t.N
		switch t.Status {
		case db.CashSliceStatusRedeemed:
			stats.RedeemedMloki, stats.RedeemedCount = t.Total, t.N
			stats.FeesEarnedMloki = t.Fees
		case db.CashSliceStatusSplit:
			stats.SplitMloki, stats.SplitCount = t.Total, t.N
		case db.CashSliceStatusExpired, db.CashSliceStatusReclaimed:
			stats.ReturnedMloki += t.Total
			stats.ReturnedCount += t.N
		case db.CashSliceStatusWrittenOff:
			stats.WrittenOffMloki, stats.WrittenOffCount = t.Total, t.N
		}
	}

	since := now.AddDate(0, 0, -cashStatsWindowDays).Truncate(24 * time.Hour)
	if err := svc.fillCashDailySeries(stats, hubID, since, now); err != nil {
		return nil, err
	}
	if err := svc.fillCashMedianTimeToRedeem(stats, hubID); err != nil {
		return nil, err
	}
	return stats, nil
}

// fillCashDailySeries buckets issued/redeemed/returned/split/written-off into
// days. Every terminal outcome gets its own bucket so the series sums back to
// the totals GetCashHubStats reports.
func (svc *appsService) fillCashDailySeries(stats *CashHubStats, hubID uint, since, now time.Time) error {
	type event struct {
		At     time.Time
		Amount int64
		Kind   string
	}
	var events []event

	// Issued is keyed on when the slice was created, redeemed on when its
	// payout settled, returned on when its bill was archived — each the moment
	// the money actually moved, not when the row happened to be written.
	if err := svc.db.Raw(`
		SELECT created_at AS at, amount_mloki AS amount, 'issued' AS kind
		FROM (`+cashClaimUnionSQL+`) u WHERE created_at >= ?
		UNION ALL
		SELECT settled_at, amount_mloki, 'redeemed'
		FROM (`+cashClaimUnionSQL+`) u2 WHERE status = 'redeemed' AND settled_at IS NOT NULL AND settled_at >= ?
		UNION ALL
		SELECT claimed_at, amount_mloki, 'returned'
		FROM (`+cashClaimUnionSQL+`) u3 WHERE status IN ('expired','reclaimed') AND claimed_at IS NOT NULL AND claimed_at >= ?
		UNION ALL
		SELECT claimed_at, amount_mloki, 'split'
		FROM (`+cashClaimUnionSQL+`) u4 WHERE status = 'split' AND claimed_at IS NOT NULL AND claimed_at >= ?
		UNION ALL
		SELECT claimed_at, amount_mloki, 'written-off'
		FROM (`+cashClaimUnionSQL+`) u5 WHERE status = 'written-off' AND claimed_at IS NOT NULL AND claimed_at >= ?
	`, now, hubID, hubID, since,
		now, hubID, hubID, since,
		now, hubID, hubID, since,
		now, hubID, hubID, since,
		now, hubID, hubID, since).Scan(&events).Error; err != nil {
		return fmt.Errorf("failed to read cash daily series for hub %d: %w", hubID, err)
	}

	buckets := map[time.Time]*CashHubDailyPoint{}
	for d := since; !d.After(now); d = d.AddDate(0, 0, 1) {
		day := d.UTC().Truncate(24 * time.Hour)
		buckets[day] = &CashHubDailyPoint{Date: day}
	}
	for _, e := range events {
		day := e.At.UTC().Truncate(24 * time.Hour)
		b, ok := buckets[day]
		if !ok {
			continue
		}
		switch e.Kind {
		case "issued":
			b.IssuedMloki += e.Amount
		case "redeemed":
			b.RedeemedMloki += e.Amount
		case "returned":
			b.ReturnedMloki += e.Amount
		case "split":
			b.SplitMloki += e.Amount
		case "written-off":
			b.WrittenOffMloki += e.Amount
		}
	}

	stats.Daily = make([]CashHubDailyPoint, 0, len(buckets))
	for _, b := range buckets {
		stats.Daily = append(stats.Daily, *b)
	}
	sort.Slice(stats.Daily, func(i, j int) bool { return stats.Daily[i].Date.Before(stats.Daily[j].Date) })
	return nil
}

// fillCashMedianTimeToRedeem measures mint-to-payout over a bounded sample of
// the most recent redemptions.
func (svc *appsService) fillCashMedianTimeToRedeem(stats *CashHubStats, hubID uint) error {
	type span struct {
		CreatedAt time.Time
		SettledAt time.Time
	}
	var spans []span
	if err := svc.db.Raw(`
		SELECT created_at, settled_at FROM (
			SELECT s.created_at AS created_at, s.settled_at AS settled_at
			FROM cash_bill_slice_archives s
			WHERE s.hub_app_id = ? AND s.outcome = 'redeemed' AND s.settled_at IS NOT NULL
			UNION ALL
			SELECT c.created_at, c.settled_at
			FROM cash_wallet_claims c
			JOIN apps a ON a.id = c.wallet_app_id
			WHERE a.parent_app_id = ? AND a.parent_kind = 'cash' AND a.kind = 'cash_wallet'
			  AND c.settled_at IS NOT NULL
		) r
		ORDER BY settled_at DESC
		LIMIT ?`, hubID, hubID, medianSampleLimit).Scan(&spans).Error; err != nil {
		return fmt.Errorf("failed to sample cash redeem times for hub %d: %w", hubID, err)
	}
	if len(spans) == 0 {
		return nil
	}
	secs := make([]int64, 0, len(spans))
	for _, s := range spans {
		if d := s.SettledAt.Sub(s.CreatedAt); d > 0 {
			secs = append(secs, int64(d.Seconds()))
		}
	}
	if len(secs) == 0 {
		return nil
	}
	sort.Slice(secs, func(i, j int) bool { return secs[i] < secs[j] })
	median := secs[len(secs)/2]
	stats.MedianTimeToRedeemSecs = &median
	return nil
}
