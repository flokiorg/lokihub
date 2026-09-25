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
	// MintedMloki is value this hub actually put into circulation.
	//
	// IssuedMloki counts every slice ever created, which is not the same
	// thing: a full split turns one bill into another and the value is
	// counted on both, so issuance inflates with churn. Minted subtracts that
	// churn and is what the operator-facing "Issued" figure means.
	MintedMloki int64

	MedianTimeToRedeemSecs *int64

	// ExpiryBuckets is filled for both scopes — the runway is as much a
	// per-hub question as a node-wide one. PerHub is node-wide only: splitting
	// one hub by hub says nothing.
	ExpiryBuckets []CashExpiryBucket
	PerHub        []CashHubOutstanding

	// Daily is a newest-last series for charting flow.
	Daily []CashHubDailyPoint
}

// CashExpiryBucket is outstanding value grouped by how soon it stops being
// redeemable — the runway an operator has before a bill either comes back as
// a reclaim or is written off.
//
// The buckets partition exactly the same slices OutstandingMloki totals, so
// they sum back to it. Anything else would be read as money appearing or
// vanishing between two figures on one screen.
type CashExpiryBucket struct {
	// Key is "24h" | "7d" | "30d" | "later" | "never", in that order.
	Key   string
	Mloki int64
	Count uint64
}

// CashHubOutstanding is one hub's share of the node's liability. Every cash
// hub appears, including those owing nothing: "which hub is carrying this" is
// only answerable if the ones carrying none are visible too.
type CashHubOutstanding struct {
	HubAppID         uint
	Name             string
	OutstandingMloki int64
	OutstandingCount uint64
	// BalanceMloki is filled by the api layer, which owns the canonical
	// balance arithmetic.
	BalanceMloki int64
}

// cashExpiryBucketKeys is the bucket order, shared with the caller so a
// renderer never has to re-derive it.
var cashExpiryBucketKeys = []string{"24h", "7d", "30d", "later", "never"}

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
// cashStatsScope selects which hubs a stats query covers: one hub's dashboard,
// or every cash hub on the node for the Cash Hubs list.
//
// It exists so the two share one implementation. The alternative — totalling
// each hub separately and summing in Go — would issue a query per hub, and
// worse, could not produce a correct median: the median of a set of medians is
// not the median of the set.
type cashStatsScope struct {
	// hubID is 0 for the node-wide scope, which is not a valid app id.
	hubID uint
}

func forCashHub(hubID uint) cashStatsScope { return cashStatsScope{hubID: hubID} }
func allCashHubs() cashStatsScope          { return cashStatsScope{} }

func (s cashStatsScope) allHubs() bool { return s.hubID == 0 }

// String makes the scope readable in the error messages these queries return.
func (s cashStatsScope) String() string {
	if s.allHubs() {
		return "all cash hubs"
	}
	return fmt.Sprintf("hub %d", s.hubID)
}

func (s cashStatsScope) union() string {
	if s.allHubs() {
		return allHubsCashClaimUnionSQL
	}
	return cashClaimUnionSQL
}

// unionArgs returns the placeholder values the union takes, in order: `now`
// for the live branch's expiry test, then the hub id once per branch — absent
// entirely for the node-wide variant, whose two hub predicates were removed.
func (s cashStatsScope) unionArgs(now time.Time) []any {
	if s.allHubs() {
		return []any{now}
	}
	return []any{now, s.hubID, s.hubID}
}

// hubPredicate is the per-hub filter for the two queries that do not go
// through the union, returned with its own argument so callers cannot pair
// the wrong one.
func (s cashStatsScope) hubPredicate(column string) (string, []any) {
	if s.allHubs() {
		return "", nil
	}
	return column + " = ? AND ", []any{s.hubID}
}

// GetAllCashHubStats totals every cash hub on the node, for the Cash Hubs
// list's own overview. Same figures as one hub's dashboard, same meanings.
func (svc *appsService) GetAllCashHubStats(now time.Time) (*CashHubStats, error) {
	return svc.cashHubStats(allCashHubs(), now)
}

func (svc *appsService) GetCashHubStats(hubID uint, now time.Time) (*CashHubStats, error) {
	return svc.cashHubStats(forCashHub(hubID), now)
}

func (svc *appsService) cashHubStats(scope cashStatsScope, now time.Time) (*CashHubStats, error) {
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
	hubWhere, hubArgs := scope.hubPredicate("a.parent_app_id")
	if err := svc.db.Raw(`
		SELECT COALESCE(SUM(c.amount_mloki), 0) AS total, COUNT(*) AS n
		FROM cash_wallet_claims c
		JOIN apps a ON a.id = c.wallet_app_id
		WHERE `+hubWhere+`a.parent_kind = 'cash' AND a.kind = 'cash_wallet'
		  AND c.claimed_at IS NULL
		  AND (a.expires_at IS NULL OR a.expires_at >= ?)`,
		append(append([]any{}, hubArgs...), now)...).Scan(&liveOutstanding).Error; err != nil {
		return nil, fmt.Errorf("failed to total outstanding cash for %s: %w", scope, err)
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
		FROM (`+scope.union()+`) u
		GROUP BY status`, scope.unionArgs(now)...).Scan(&totals).Error; err != nil {
		return nil, fmt.Errorf("failed to total cash outcomes for %s: %w", scope, err)
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

	// Minted is issuance with full-split churn removed.
	//
	// A full split marks exactly one source slice terminal as 'split' and
	// creates a carved bill carrying the same value, so each split adds that
	// value to Issued twice; subtracting Split leaves it counted once, and
	// the same holds down a chain of splits, each hop adding one of each.
	// A PARTIAL split needs no correction and gets none: it reduces the
	// source slice in place rather than marking it terminal, so the value is
	// only ever counted once to begin with.
	//
	// Reconciled against the bill archive's independent lineage column,
	// SplitFromWalletAppID, and the two agree only once consolidation is
	// accounted for — which is why this formula is the right one rather than
	// the lineage one. On real data here, of 115,430,000 in split-status
	// slices, 96,856,000 went into a newly carved bill and 18,574,000 went
	// into an ALREADY EXISTING bill. That second figure is cash_consolidate,
	// and a consolidation target carries no SplitFromWalletAppID, so summing
	// lineage misses it and under-counts churn by exactly that amount.
	// Subtracting Split catches both, because both mark their source slice
	// terminal as 'split'.
	stats.MintedMloki = stats.IssuedMloki - stats.SplitMloki

	if err := svc.fillCashExpiryBuckets(stats, scope, now); err != nil {
		return nil, err
	}
	if scope.allHubs() {
		if err := svc.fillCashPerHubOutstanding(stats, now); err != nil {
			return nil, err
		}
	}

	since := now.AddDate(0, 0, -cashStatsWindowDays).Truncate(24 * time.Hour)
	if err := svc.fillCashDailySeries(stats, scope, since, now); err != nil {
		return nil, err
	}
	if err := svc.fillCashMedianTimeToRedeem(stats, scope); err != nil {
		return nil, err
	}
	return stats, nil
}

// fillCashDailySeries buckets issued/redeemed/returned/split/written-off into
// days. Every terminal outcome gets its own bucket so the series sums back to
// the totals GetCashHubStats reports.
func (svc *appsService) fillCashDailySeries(stats *CashHubStats, scope cashStatsScope, since, now time.Time) error {
	type event struct {
		At     time.Time
		Amount int64
		Kind   string
	}
	var events []event

	// Issued is keyed on when the slice was created, redeemed on when its
	// payout settled, returned on when its bill was archived — each the moment
	// the money actually moved, not when the row happened to be written.
	// Issued, redeemed and split come off the union, keyed on the moment the
	// money moved.
	//
	// Returned and written-off deliberately do NOT. Those two used to be read
	// from the union gated on `claimed_at IS NOT NULL`, which can never match:
	// DeriveSliceOutcome only ever assigns expired/reclaimed/written-off in
	// the branch where ClaimedAt is nil — by construction, a slice that
	// expired or was written off was never claimed, which is exactly what
	// distinguishes it from redeemed/split. Both buckets were therefore
	// structurally always zero, on every hub, forever, while the headline
	// totals beside them were correct. On this node alone that silently hid
	// 3,849 real slices.
	//
	// They are archive-only events by nature — value comes back when the bill
	// is destroyed, not when a recipient acts — so they are read straight from
	// the slice archive and keyed on archived_at, the moment the value
	// actually returned. A LIVE slice showing status 'expired' has not
	// returned anything yet: its window has passed but the sweep has not
	// collected it, so it correctly contributes nothing here.
	union := scope.union()
	var args []any
	for range 3 {
		args = append(args, scope.unionArgs(now)...)
		args = append(args, since)
	}
	archivedWhere, archivedArgs := scope.hubPredicate("s.hub_app_id")
	for range 2 {
		args = append(args, archivedArgs...)
		args = append(args, since)
	}
	if err := svc.db.Raw(`
		SELECT created_at AS at, amount_mloki AS amount, 'issued' AS kind
		FROM (`+union+`) u WHERE created_at >= ?
		UNION ALL
		SELECT settled_at, amount_mloki, 'redeemed'
		FROM (`+union+`) u2 WHERE status = 'redeemed' AND settled_at IS NOT NULL AND settled_at >= ?
		UNION ALL
		SELECT claimed_at, amount_mloki, 'split'
		FROM (`+union+`) u4 WHERE status = 'split' AND claimed_at IS NOT NULL AND claimed_at >= ?
		UNION ALL
		SELECT s.archived_at, s.amount_mloki, 'returned'
		FROM cash_bill_slice_archives s
		WHERE `+archivedWhere+`s.outcome IN ('expired','reclaimed') AND s.archived_at >= ?
		UNION ALL
		SELECT s.archived_at, s.amount_mloki, 'written-off'
		FROM cash_bill_slice_archives s
		WHERE `+archivedWhere+`s.outcome = 'written-off' AND s.archived_at >= ?
	`, args...).Scan(&events).Error; err != nil {
		return fmt.Errorf("failed to read cash daily series for %s: %w", scope, err)
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

// expiryBucketKey classifies one bill by how long it has left to be redeemed.
//
// Split out from the query that feeds it so the boundaries can be tested
// directly. Two of them are otherwise unreachable: a nil deadline is filtered
// in SQL, and a deadline already in the past is excluded by the outstanding
// query's own WHERE clause. That second case is exactly why the guard exists —
// without it a negative duration would fall through to the first "less than"
// arm and file already-expired value under "expires within a day", which is
// the opposite of true. A future edit to that WHERE clause should not be able
// to introduce that silently.
func expiryBucketKey(expiresAt *time.Time, now time.Time) string {
	if expiresAt == nil {
		return "never"
	}
	switch left := expiresAt.Sub(now); {
	case left < 0:
		return "24h"
	case left < 24*time.Hour:
		return "24h"
	case left < 7*24*time.Hour:
		return "7d"
	case left < 30*24*time.Hour:
		return "30d"
	default:
		return "later"
	}
}

// fillCashExpiryBuckets groups the outstanding liability by time left to
// redeem it.
//
// The WHERE clause is deliberately identical to the outstanding total's own,
// so the buckets are a partition of that figure rather than a second,
// slightly different population. Bucketing happens in Go: the boundaries are
// relative to now, and expressing that in SQL means date arithmetic that
// differs between sqlite and Postgres for no benefit.
func (svc *appsService) fillCashExpiryBuckets(stats *CashHubStats, scope cashStatsScope, now time.Time) error {
	var rows []struct {
		AmountMloki int64
		ExpiresAt   *time.Time
	}
	hubWhere, hubArgs := scope.hubPredicate("a.parent_app_id")
	if err := svc.db.Raw(`
		SELECT c.amount_mloki AS amount_mloki, a.expires_at AS expires_at
		FROM cash_wallet_claims c
		JOIN apps a ON a.id = c.wallet_app_id
		WHERE `+hubWhere+`a.parent_kind = 'cash' AND a.kind = 'cash_wallet'
		  AND c.claimed_at IS NULL
		  AND (a.expires_at IS NULL OR a.expires_at >= ?)`,
		append(append([]any{}, hubArgs...), now)...).Scan(&rows).Error; err != nil {
		return fmt.Errorf("failed to bucket outstanding cash by expiry for %s: %w", scope, err)
	}

	byKey := map[string]*CashExpiryBucket{}
	for _, k := range cashExpiryBucketKeys {
		byKey[k] = &CashExpiryBucket{Key: k}
	}
	for _, r := range rows {
		key := expiryBucketKey(r.ExpiresAt, now)
		byKey[key].Mloki += r.AmountMloki
		byKey[key].Count++
	}

	stats.ExpiryBuckets = make([]CashExpiryBucket, 0, len(cashExpiryBucketKeys))
	for _, k := range cashExpiryBucketKeys {
		stats.ExpiryBuckets = append(stats.ExpiryBuckets, *byKey[k])
	}
	return nil
}

// fillCashPerHubOutstanding splits the liability by hub.
//
// Driven from the apps table rather than from claims, so a hub owing nothing
// still appears: on a list page the question is "which hub is carrying this",
// and a hub that drops out of the answer looks deleted rather than settled.
func (svc *appsService) fillCashPerHubOutstanding(stats *CashHubStats, now time.Time) error {
	var rows []CashHubOutstanding
	if err := svc.db.Raw(`
		SELECT h.id AS hub_app_id, h.name AS name,
		       COALESCE(SUM(CASE WHEN c.id IS NOT NULL THEN c.amount_mloki ELSE 0 END), 0) AS outstanding_mloki,
		       COALESCE(SUM(CASE WHEN c.id IS NOT NULL THEN 1 ELSE 0 END), 0) AS outstanding_count
		FROM apps h
		LEFT JOIN apps a
		       ON a.parent_app_id = h.id AND a.parent_kind = 'cash' AND a.kind = 'cash_wallet'
		      AND (a.expires_at IS NULL OR a.expires_at >= ?)
		LEFT JOIN cash_wallet_claims c
		       ON c.wallet_app_id = a.id AND c.claimed_at IS NULL
		WHERE h.kind = 'cash_hub'
		GROUP BY h.id, h.name
		ORDER BY outstanding_mloki DESC, h.id`, now).Scan(&rows).Error; err != nil {
		return fmt.Errorf("failed to split outstanding cash by hub: %w", err)
	}
	stats.PerHub = rows
	return nil
}

// fillCashMedianTimeToRedeem measures mint-to-payout over a bounded sample of
// the most recent redemptions.
func (svc *appsService) fillCashMedianTimeToRedeem(stats *CashHubStats, scope cashStatsScope) error {
	type span struct {
		CreatedAt time.Time
		SettledAt time.Time
	}
	var spans []span
	archivedWhere, archivedArgs := scope.hubPredicate("s.hub_app_id")
	liveWhere, liveArgs := scope.hubPredicate("a.parent_app_id")
	args := append(append([]any{}, archivedArgs...), liveArgs...)
	args = append(args, medianSampleLimit)
	if err := svc.db.Raw(`
		SELECT created_at, settled_at FROM (
			SELECT s.created_at AS created_at, s.settled_at AS settled_at
			FROM cash_bill_slice_archives s
			WHERE `+archivedWhere+`s.outcome = 'redeemed' AND s.settled_at IS NOT NULL
			UNION ALL
			SELECT c.created_at, c.settled_at
			FROM cash_wallet_claims c
			JOIN apps a ON a.id = c.wallet_app_id
			WHERE `+liveWhere+`a.parent_kind = 'cash' AND a.kind = 'cash_wallet'
			  AND c.settled_at IS NOT NULL
		) r
		ORDER BY settled_at DESC
		LIMIT ?`, args...).Scan(&spans).Error; err != nil {
		return fmt.Errorf("failed to sample cash redeem times for %s: %w", scope, err)
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
