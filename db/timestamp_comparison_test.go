package db_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/migrations"
)

// TestTimestampComparisonsMatchGo guards a sharp edge found while materialising
// CashBillArchive.RetainedUntil.
//
// The sqlite driver stores a time.Time as Go's own String() rendering, monotonic
// suffix included:
//
//	2026-09-25 22:43:42.080289683 +0000 UTC m=-3599.937755778
//
// sqlite's date FUNCTIONS cannot parse that — datetime(ended_at, '+N seconds')
// returns NULL rather than erroring — which is why every deadline calculation in
// this package is done in Go.
//
// Comparisons against a bound parameter are a different matter: they go through
// the driver's typed binding rather than that text, and they are sound. This test
// pins that, because the retention paths depend on it for money decisions —
// service.PruneExpiredSpentBills asks "retained_until <= now" and
// retainedSpentBillPubkeys asks "retained_until > now", and a driver change that
// silently degraded either to text comparison would drop bills from, or revive
// them into, the answerable set.
//
// The awkward cases are the point: differing fractional-digit counts, a value in
// a non-UTC zone, and one carrying a monotonic reading.
func TestTimestampComparisonsMatchGo(t *testing.T) {
	uri := filepath.Join(t.TempDir(), "tsprobe.db")
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: uri})
	require.NoError(t, err)
	require.NoError(t, migrations.Migrate(gormDB))

	app := db.App{Name: "h", AppPubkey: "p", Kind: db.AppKindCashHub}
	require.NoError(t, gormDB.Create(&app).Error)
	require.NoError(t, gormDB.Create(&db.CashHubConfig{AppID: app.ID, SpentRetentionSecs: 3600}).Error)

	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	// Deliberately awkward: differing fractional-digit counts, a value in a
	// non-UTC zone, and one carrying a monotonic reading (time.Now()).
	cases := []struct {
		name     string
		deadline time.Time
	}{
		{"whole second", base},
		{"two frac digits", base.Add(80 * time.Millisecond)},
		{"one frac digit", base.Add(100 * time.Millisecond)},
		{"nine frac digits", base.Add(123456789 * time.Nanosecond)},
		{"non-utc zone", base.Add(2 * time.Second).In(time.FixedZone("CEST", 2*60*60))},
		{"monotonic now", time.Now().Add(time.Hour)},
	}

	for i, c := range cases {
		require.NoError(t, gormDB.Create(&db.CashBillArchive{
			WalletAppID: uint(i + 1), HubAppID: app.ID,
			WalletPubkey: c.name, EndedAt: base, RetainedUntil: &c.deadline,
			Outcome: db.CashBillOutcomeExpired,
		}).Error)
	}

	// For each case, ask the DB the same question Go can answer directly, at
	// three probe points around the stored deadline.
	for _, c := range cases {
		for _, offset := range []time.Duration{-time.Millisecond, 0, time.Millisecond} {
			probe := c.deadline.Add(offset)
			wantFuture := c.deadline.After(probe)

			var count int64
			require.NoError(t, gormDB.Model(&db.CashBillArchive{}).
				Where("wallet_pubkey = ? AND retained_until > ?", c.name, probe).
				Count(&count).Error)
			gotFuture := count == 1

			require.Equal(t, wantFuture, gotFuture,
				"SQL and Go must agree on %q at offset %s: a timestamp comparison that "+
					"disagrees with time.After would move bills in or out of the answerable set",
				c.name, offset)
		}
	}
}
