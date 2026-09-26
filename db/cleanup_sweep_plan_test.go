package db_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	testdb "github.com/flokiorg/lokihub/tests/db"
)

// TestCleanupSweepUsesPartialIndex pins that the cash cleanup sweep's query can
// actually use idx_apps_cleanup, the partial index added for it.
//
// Worth asserting rather than assuming, because the two do not obviously match:
// the sweep filters
//
//	parent_app_id IS NOT NULL AND expires_at < ? AND cleanup_in_progress = ?
//
// while the index is on apps(expires_at) with the predicate
//
//	parent_app_id IS NOT NULL AND cleanup_in_progress = false
//
// so the planner has to prove the query implies the predicate. If it ever stops
// doing so — a reworded WHERE clause, a dropped index, a driver that binds the
// boolean differently — the sweep quietly becomes a full scan of the apps table
// every five minutes, growing with every bill and circle wallet the hub has ever
// issued. Nothing would fail; it would just get slower forever.
func TestCleanupSweepUsesPartialIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds twenty thousand apps")
	}

	gormDB, err := testdb.NewDB(t)
	require.NoError(t, err)
	defer testdb.CloseDB(gormDB)

	// Silences the handle's query echoing while seeding.
	testdb.CountStatements(gormDB)
	seedSweepApps(t, gormDB)

	plan := explainSweepQuery(t, gormDB)
	require.Contains(t, plan, "idx_apps_cleanup",
		"the cleanup sweep must use its partial index; plan was:\n%s", plan)
}

// seedSweepApps writes a realistic distribution: almost every sub-wallet still
// live, a few expired.
//
// The distribution is load-bearing. With every row expired, a full scan is
// genuinely the cheapest plan — LIMIT 200 is satisfied from the first rows — so a
// fixture that expires everything reports a scan and tells you nothing about
// whether the index works when it matters.
func seedSweepApps(t *testing.T, gormDB *gorm.DB) {
	t.Helper()

	hub := db.App{Name: "sweep-hub", AppPubkey: "sweep-hub-pubkey", Kind: db.AppKindCashHub}
	require.NoError(t, gormDB.Create(&hub).Error)

	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-time.Hour)

	const total = 20_000
	apps := make([]db.App, total)
	for i := range apps {
		expiry := future
		if i%400 == 0 {
			expiry = past
		}
		apps[i] = db.App{
			Name:        fmt.Sprintf("sweep-w%d", i),
			AppPubkey:   fmt.Sprintf("%064x", i),
			Kind:        db.AppKindCashWallet,
			ParentAppID: &hub.ID,
			ParentKind:  db.ParentKindCash,
			ExpiresAt:   &expiry,
		}
	}
	require.NoError(t, gormDB.CreateInBatches(apps, 500).Error)

	// Without statistics the planner guesses, and its guess is not what runs in
	// production.
	require.NoError(t, gormDB.Exec("ANALYZE").Error)
}

// explainSweepQuery returns the query plan for the sweep's own SELECT, as one
// string. The query mirrors service.runCashCleanup; keep them in step.
func explainSweepQuery(t *testing.T, gormDB *gorm.DB) string {
	t.Helper()

	const q = `SELECT id FROM apps
		WHERE parent_app_id IS NOT NULL AND expires_at < ? AND cleanup_in_progress = ?
		LIMIT 200`

	if gormDB.Dialector.Name() == "postgres" {
		var lines []string
		require.NoError(t, gormDB.Raw("EXPLAIN "+q, time.Now(), false).Scan(&lines).Error)
		return strings.Join(lines, "\n")
	}

	var rows []struct {
		ID, Parent, NotUsed int
		Detail              string
	}
	require.NoError(t, gormDB.Raw("EXPLAIN QUERY PLAN "+q, time.Now(), false).Scan(&rows).Error)
	details := make([]string, 0, len(rows))
	for _, r := range rows {
		details = append(details, r.Detail)
	}
	return strings.Join(details, "\n")
}
