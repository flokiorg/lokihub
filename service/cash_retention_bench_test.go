package service

import (
	"encoding/hex"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	testdb "github.com/flokiorg/lokihub/tests/db"
)

// Baseline for the spent-bill retention paths, which are the DB half of
// data/docs/design/subwallet-scale-benchmark-and-redesign-2026-09-25.md.
//
// Both paths below walk every row of cash_bill_archives and then ask two more
// questions per row (db.SpentBillRetainedUntil fetches the archive row, then
// its hub config). Statements are therefore the number that matters, not
// nanoseconds: on sqlite a round trip is a function call, so an N+1 hides in a
// timing and only shows up as a count.

// archiveLadder is smaller at the top than the in-memory ladder: seeding a
// million archive rows through gorm costs minutes, and the curve's shape is
// already unambiguous by 50k.
var archiveLadder = []int{100, 1_000, 10_000, 50_000}

// seedArchive writes n destroyed-bill rows for one hub whose retention window
// is still open, and returns their wallet pubkeys.
func seedArchive(tb testing.TB, gormDB *gorm.DB, n int) []string {
	tb.Helper()

	hubApp := db.App{Name: "bench hub", AppPubkey: randHex(tb, 32), Kind: db.AppKindCashHub}
	require.NoError(tb, gormDB.Create(&hubApp).Error)
	require.NoError(tb, gormDB.Create(&db.CashHubConfig{
		AppID: hubApp.ID,
		// Wide enough that every seeded row is still answerable, so the sweep
		// does the most work rather than short-circuiting.
		SpentRetentionSecs: int(time.Hour.Seconds() * 24 * 15),
	}).Error)

	pubkeys := make([]string, n)
	rows := make([]db.CashBillArchive, n)
	for i := range rows {
		pubkeys[i] = randHex(tb, 32)
		rows[i] = db.CashBillArchive{
			WalletAppID:  uint(i + 1),
			HubAppID:     hubApp.ID,
			WalletPubkey: pubkeys[i],
			MintedAt:     time.Now().Add(-2 * time.Hour),
			EndedAt:      time.Now().Add(-time.Hour),
			Outcome:      db.CashBillOutcomeExpired,
			TotalMloki:   1000,
		}
	}
	// One multi-row insert per 500 rather than per row: seeding is not what is
	// being measured and should not dominate the runtime.
	require.NoError(tb, gormDB.CreateInBatches(rows, 500).Error)

	return pubkeys
}

func randHex(tb testing.TB, n int) string {
	tb.Helper()
	buf := make([]byte, n)
	//nolint:gosec // fixture data, not a key
	if _, err := rand.New(rand.NewSource(time.Now().UnixNano())).Read(buf); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(buf)
}

func benchService(tb testing.TB, n int) (*service, *testdb.StatementCounter, []string) {
	tb.Helper()

	gormDB, err := testdb.NewDB(tb)
	require.NoError(tb, err)
	tb.Cleanup(func() { testdb.CloseDB(gormDB) })

	// Attached BEFORE seeding, which also silences it: the handle is built
	// with LogQueries:true, so echoing tens of thousands of seed inserts would
	// bury the result and slow the run down more than the work being measured.
	counter := testdb.CountStatements(gormDB)

	pubkeys := seedArchive(tb, gormDB, n)

	// A freshly created database has no statistics; ANALYZE makes this measure
	// the steady state rather than a planner guessing. Both dialects support it.
	require.NoError(tb, gormDB.Exec("ANALYZE").Error)
	counter.Reset()

	svc := &service{db: gormDB, walletRegistry: newWalletRegistry()}
	svc.walletRegistry.Add(pubkeys...)

	return svc, counter, pubkeys
}

// --- Op 8: the two-query primitive ------------------------------------------

func BenchmarkSpentBillRetainedUntil(b *testing.B) {
	for _, n := range archiveLadder {
		b.Run(fmt.Sprintf("archive=%d", n), func(b *testing.B) {
			svc, counter, pubkeys := benchService(b, n)
			probe := pubkeys[n/2]

			counter.Reset()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, ok := db.SpentBillRetainedUntil(svc.db, probe); !ok {
					b.Fatal("expected the bill to still be answerable")
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(counter.N())/float64(b.N), "stmts/op")
		})
	}
}

// --- Op 9: the startup reload ------------------------------------------------
//
// This runs before the hub serves anything, so its cost is time-to-first-
// served-event, not merely CPU.

func BenchmarkRetainedSpentBillPubkeys(b *testing.B) {
	for _, n := range archiveLadder {
		b.Run(fmt.Sprintf("archive=%d", n), func(b *testing.B) {
			svc, counter, _ := benchService(b, n)

			counter.Reset()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got := len(svc.retainedSpentBillPubkeys()); got != n {
					b.Fatalf("reloaded %d wallets, want %d", got, n)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(counter.N())/float64(b.N), "stmts/op")
		})
	}
}

// --- Op 10: the periodic sweep ----------------------------------------------
//
// Every 5 minutes in production. Seeded so every row is still retained, which
// is the steady state: the sweep does its full walk and removes nothing.

func BenchmarkPruneExpiredSpentBills(b *testing.B) {
	for _, n := range archiveLadder {
		b.Run(fmt.Sprintf("archive=%d", n), func(b *testing.B) {
			svc, counter, _ := benchService(b, n)

			counter.Reset()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				svc.PruneExpiredSpentBills()
			}
			b.StopTimer()
			b.ReportMetric(float64(counter.N())/float64(b.N), "stmts/op")
		})
	}
}

// TestRetentionStatementsGrowWithArchiveSize pins the N+1 the scale harness
// measured, so the shape is a fact in CI rather than a claim in a document.
//
// Both paths walk every archived bill and then ask two further questions per
// row (db.SpentBillRetainedUntil reads the archive row, then its hub config),
// so statements come to 2N+1. At a million archived bills that is two million
// statements per startup and per five-minute sweep.
//
// This is a characterisation test. When the materialised retained_until column
// lands, invert it: statements must stop growing with row count, which is the
// acceptance criterion in
// data/docs/design/subwallet-scale-benchmark-and-redesign-2026-09-25.md.
func TestRetentionStatementsGrowWithArchiveSize(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds thousands of archive rows")
	}

	measure := func(n int) (reload, prune int64) {
		gormDB, err := testdb.NewDB(t)
		require.NoError(t, err)
		defer testdb.CloseDB(gormDB)

		counter := testdb.CountStatements(gormDB)
		pubkeys := seedArchive(t, gormDB, n)

		svc := &service{db: gormDB, walletRegistry: newWalletRegistry()}
		svc.walletRegistry.Add(pubkeys...)

		counter.Reset()
		require.Len(t, svc.retainedSpentBillPubkeys(), n)
		reload = counter.N()

		counter.Reset()
		svc.PruneExpiredSpentBills()
		prune = counter.N()

		return reload, prune
	}

	smallReload, smallPrune := measure(100)
	bigReload, bigPrune := measure(1_000)

	// 2N+1: one pluck, then two per candidate.
	require.EqualValues(t, 201, smallReload)
	require.EqualValues(t, 2_001, bigReload)
	require.EqualValues(t, 201, smallPrune)
	require.EqualValues(t, 2_001, bigPrune)

	require.Greater(t, bigReload, smallReload*5,
		"startup reload statements scale with the archive; the fix must make this constant")
	require.Greater(t, bigPrune, smallPrune*5,
		"the sweep's statements scale with the archive; the fix must make this constant")
}
