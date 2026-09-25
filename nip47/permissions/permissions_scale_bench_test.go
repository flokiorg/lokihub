package permissions

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	testdb "github.com/flokiorg/lokihub/tests/db"
)

// Baseline for the permission reads on the per-request path, for
// data/docs/design/subwallet-scale-benchmark-and-redesign-2026-09-25.md.
//
// HasPermission and GetPermittedMethods both filter app_permissions by app_id,
// and they run for every NWC request. AppPermission.AppId carries no index:
// gorm's constraint:OnDelete:CASCADE declares a foreign key, and neither
// postgres nor sqlite indexes the referencing side of one. So the question these
// benchmarks answer is whether every request scans the whole table.
//
// Flat across the ladder means an index is being used. Linear means one is
// missing, and the cost lands on every request the hub serves.

var permLadder = []int{100, 1_000, 10_000, 50_000}

// seedPermissions creates n apps, each with two permission rows, and returns
// one app in the middle of the table to probe.
func seedPermissions(tb testing.TB, gormDB *gorm.DB, n int) *db.App {
	tb.Helper()

	apps := make([]db.App, n)
	for i := range apps {
		apps[i] = db.App{
			Name:      fmt.Sprintf("app %d", i),
			AppPubkey: fmt.Sprintf("%064x", i),
			Kind:      db.AppKindIsolated,
		}
	}
	require.NoError(tb, gormDB.CreateInBatches(apps, 500).Error)

	expires := time.Now().Add(24 * time.Hour)
	perms := make([]db.AppPermission, 0, n*2)
	for i := range apps {
		perms = append(perms,
			db.AppPermission{AppId: apps[i].ID, Scope: constants.PAY_INVOICE_SCOPE, MaxAmountLoki: 1000, ExpiresAt: &expires},
			db.AppPermission{AppId: apps[i].ID, Scope: constants.GET_BALANCE_SCOPE, ExpiresAt: &expires},
		)
	}
	require.NoError(tb, gormDB.CreateInBatches(perms, 500).Error)

	return &apps[n/2]
}

func benchPermSvc(tb testing.TB, n int) (*permissionsService, *testdb.StatementCounter, *db.App) {
	tb.Helper()

	gormDB, err := testdb.NewDB(tb)
	require.NoError(tb, err)
	tb.Cleanup(func() { testdb.CloseDB(gormDB) })

	// Before seeding, so LogQueries:true does not echo every insert.
	counter := testdb.CountStatements(gormDB)
	app := seedPermissions(tb, gormDB, n)

	// See benchCircleDB: without statistics the planner guesses.
	require.NoError(tb, gormDB.Exec("ANALYZE").Error)
	counter.Reset()

	return &permissionsService{db: gormDB}, counter, app
}

// BenchmarkHasPermission is the per-request authorisation check.
func BenchmarkHasPermission(b *testing.B) {
	for _, n := range permLadder {
		b.Run(fmt.Sprintf("apps=%d", n), func(b *testing.B) {
			svc, counter, app := benchPermSvc(b, n)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ok, _, _ := svc.HasPermission(app, constants.PAY_INVOICE_SCOPE)
				if !ok {
					b.Fatal("expected the scope to be permitted")
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(counter.N())/float64(b.N), "stmts/op")
		})
	}
}

// BenchmarkGetPermittedMethodsQuery covers the other per-request read. It calls
// the query directly rather than GetPermittedMethods, which needs an LNClient
// to filter by supported methods — the scan being measured is the same one.
func BenchmarkGetPermittedMethodsQuery(b *testing.B) {
	for _, n := range permLadder {
		b.Run(fmt.Sprintf("apps=%d", n), func(b *testing.B) {
			svc, counter, app := benchPermSvc(b, n)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				perms := []db.AppPermission{}
				if err := svc.db.Where("app_id = ?", app.ID).Find(&perms).Error; err != nil {
					b.Fatal(err)
				}
				if len(perms) != 2 {
					b.Fatalf("found %d permissions, want 2", len(perms))
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(counter.N())/float64(b.N), "stmts/op")
		})
	}
}
