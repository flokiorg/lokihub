package queries_test

import (
	"encoding/hex"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	testdb "github.com/flokiorg/lokihub/tests/db"
)

// Baseline for the circle-side queries in
// data/docs/design/subwallet-scale-benchmark-and-redesign-2026-09-25.md.
//
// Circles matter here for the same reason cash does: a circle issues one
// sub-wallet per member, every one of which lands in the same wallet registry
// as a cash bill (create_app_consumer derives a wallet pubkey for every app
// kind). The registry cost is therefore already measured by the service-side
// benchmarks; what is specific to circles is the aggregate read below, which
// runs inside the advisory-locked transaction that admits a member — so its
// cost is serialised against every other join on that circle.

var memberLadder = []int{100, 1_000, 10_000, 50_000}

// seedCircle creates a circle hub with n member wallets, each carrying a
// pay_invoice permission with a spend cap, which is what the commitment sum
// aggregates.
func seedCircle(tb testing.TB, gormDB *gorm.DB, n int) uint {
	tb.Helper()

	hub := db.App{Name: "bench circle hub", AppPubkey: benchHex(tb), Kind: db.AppKindCircleHub}
	require.NoError(tb, gormDB.Create(&hub).Error)

	expires := time.Now().Add(24 * time.Hour)
	members := make([]db.App, n)
	for i := range members {
		members[i] = db.App{
			Name:        fmt.Sprintf("member %d", i),
			AppPubkey:   benchHex(tb),
			Kind:        db.AppKindCircleWallet,
			ParentAppID: &hub.ID,
			ParentKind:  db.ParentKindCircle,
			ExpiresAt:   &expires,
		}
	}
	require.NoError(tb, gormDB.CreateInBatches(members, 500).Error)

	perms := make([]db.AppPermission, n)
	for i := range members {
		perms[i] = db.AppPermission{
			AppId:         members[i].ID,
			Scope:         constants.PAY_INVOICE_SCOPE,
			MaxAmountLoki: 1000,
			ExpiresAt:     &expires,
		}
	}
	require.NoError(tb, gormDB.CreateInBatches(perms, 500).Error)

	return hub.ID
}

func benchHex(tb testing.TB) string {
	tb.Helper()
	buf := make([]byte, 32)
	//nolint:gosec // fixture data, not a key
	if _, err := rand.New(rand.NewSource(time.Now().UnixNano())).Read(buf); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(buf)
}

func benchCircleDB(tb testing.TB, n int) (*gorm.DB, *testdb.StatementCounter, uint) {
	tb.Helper()

	gormDB, err := testdb.NewDB(tb)
	require.NoError(tb, err)
	tb.Cleanup(func() { testdb.CloseDB(gormDB) })

	// Before seeding, so the handle's LogQueries:true does not echo every
	// insert into the benchmark output.
	counter := testdb.CountStatements(gormDB)
	hubID := seedCircle(tb, gormDB, n)
	counter.Reset()

	return gormDB, counter, hubID
}

// --- Op 13: the commitment sum, inside the member-admission lock -------------

func BenchmarkGetCircleCommitmentMloki(b *testing.B) {
	for _, n := range memberLadder {
		b.Run(fmt.Sprintf("members=%d", n), func(b *testing.B) {
			gormDB, counter, hubID := benchCircleDB(b, n)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := queries.GetCircleCommitmentMloki(gormDB, hubID); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(counter.N())/float64(b.N), "stmts/op")
		})
	}
}

// --- Op 14: the batched balance read ----------------------------------------
//
// One statement however many app ids it is handed — an N+1 that was already
// fixed, and this is what stops it regressing.
//
// The ladder stops below memberLadder's top rung on purpose: every id is bound
// as a separate SQL variable, so the query fails outright past the driver's
// parameter ceiling. See
// TestGetIsolatedBalancesByAppIDsHitsDriverVariableLimit.
var balanceLadder = []int{100, 1_000, 10_000}

func BenchmarkGetIsolatedBalancesByAppIDs(b *testing.B) {
	for _, n := range balanceLadder {
		b.Run(fmt.Sprintf("apps=%d", n), func(b *testing.B) {
			gormDB, counter, hubID := benchCircleDB(b, n)

			var appIDs []uint
			require.NoError(b, gormDB.Model(&db.App{}).
				Where("parent_app_id = ?", hubID).
				Pluck("id", &appIDs).Error)
			counter.Reset()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := queries.GetIsolatedBalancesByAppIDs(gormDB, appIDs); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(counter.N())/float64(b.N), "stmts/op")
		})
	}
}

// TestGetIsolatedBalancesByAppIDsHitsDriverVariableLimit pins a limitation the
// scale harness found: every app id becomes its own SQL bind variable, so the
// query fails once the caller has more children than the driver allows —
// roughly 32k on sqlite, 65k on postgres.
//
// It fails, rather than degrading: a hub past that many bills, or a circle past
// that many members, gets an error from its stats and children endpoints
// (api/cash_hub_stats.go, api/circle_hub_stats.go, api.go's children listing)
// rather than a slow answer.
//
// This is a characterisation test, asserting today's behaviour so the boundary
// is discoverable. When the chunked (or join-based) fix lands, invert it: the
// call should succeed at any size.
func TestGetIsolatedBalancesByAppIDsHitsDriverVariableLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds tens of thousands of rows")
	}

	gormDB, err := testdb.NewDB(t)
	require.NoError(t, err)
	defer testdb.CloseDB(gormDB)

	// Silences the handle's query echoing while seeding.
	testdb.CountStatements(gormDB)

	appIDs := make([]uint, 50_000)
	for i := range appIDs {
		appIDs[i] = uint(i + 1)
	}

	_, err = queries.GetIsolatedBalancesByAppIDs(gormDB, appIDs)
	require.Error(t, err, "50k bind variables must still be beyond the driver; if this passes, the chunking fix landed and this test should be inverted")
	require.Contains(t, err.Error(), "too many SQL variables")
}
