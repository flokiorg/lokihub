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

	// A freshly created database has no statistics, so the planner guesses and
	// can pick a plan it would never choose in production. ANALYZE makes this
	// measure the steady state. Supported by both dialects.
	require.NoError(tb, gormDB.Exec("ANALYZE").Error)
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
// Chunked internally, so the statement count is ceil(n/balanceChunkSize) rather
// than one. The top rung is the case the chunking exists for: passing every id
// as its own SQL variable used to fail outright with "too many SQL variables"
// past the driver ceiling (~32k sqlite, ~65k postgres), which broke the Cash Hub
// dashboard, Circle Hub stats and the children listing for any provider that
// large.
var balanceLadder = []int{100, 1_000, 10_000, 50_000}

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
