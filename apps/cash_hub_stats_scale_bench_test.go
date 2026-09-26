package apps

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	testdb "github.com/flokiorg/lokihub/tests/db"
)

// Op 18 of data/docs/design/subwallet-scale-benchmark-and-redesign-2026-09-25.md:
// the Cash Hub dashboard at a large bill count.
//
// Unlike the paths fixed so far, this one is an aggregate by nature — it SUMs
// value across a hub's slices — so it cannot become a single indexed lookup. What
// the benchmark is for is the constant and the shape: whether the totals are
// computed from indexes or from table scans, and how many statements a dashboard
// load costs.

var statsLadder = []int{1_000, 10_000, 50_000}

// seedHubWithBills writes a hub with n bills, each carrying one claim, split
// across the outcome buckets the dashboard reports so every branch of the union
// does work.
func seedHubWithBills(tb testing.TB, gormDB *gorm.DB, n int) uint {
	tb.Helper()

	hub := db.App{Name: "stats-hub", AppPubkey: "stats-hub-pubkey", Kind: db.AppKindCashHub}
	require.NoError(tb, gormDB.Create(&hub).Error)
	require.NoError(tb, gormDB.Create(&db.CashHubConfig{
		AppID: hub.ID, PerWalletMaxMloki: 100_000, SpentRetentionSecs: 86_400,
	}).Error)

	future := time.Now().Add(24 * time.Hour)
	bills := make([]db.App, n)
	for i := range bills {
		bills[i] = db.App{
			Name:        fmt.Sprintf("stats-bill-%d", i),
			AppPubkey:   fmt.Sprintf("%064x", i),
			Kind:        db.AppKindCashWallet,
			ParentAppID: &hub.ID,
			ParentKind:  db.ParentKindCash,
			ExpiresAt:   &future,
		}
	}
	require.NoError(tb, gormDB.CreateInBatches(bills, 500).Error)

	now := time.Now()
	claims := make([]db.CashWalletClaim, n)
	for i := range bills {
		claim := db.CashWalletClaim{
			WalletAppID:   bills[i].ID,
			IdentityType:  db.CashIdentityCash,
			IdentityValue: fmt.Sprintf("id-%d", i),
			AmountMloki:   1_000,
		}
		// A third claimed and settled (terminal, so the union counts it as
		// redeemed), the rest still outstanding — both the live-outstanding
		// query and the outcome union then have rows to sum.
		if i%3 == 0 {
			claimedAt := now.Add(-time.Hour)
			claim.ClaimedAt = &claimedAt
			claim.SettledAt = &claimedAt
			claim.PaymentHash = fmt.Sprintf("hash-%d", i)
			claim.Preimage = fmt.Sprintf("preimage-%d", i)
		}
		claims[i] = claim
	}
	require.NoError(tb, gormDB.CreateInBatches(claims, 500).Error)

	require.NoError(tb, gormDB.Exec("ANALYZE").Error)
	return hub.ID
}

func benchStatsService(tb testing.TB, n int) (*appsService, *testdb.StatementCounter, uint) {
	tb.Helper()

	gormDB, err := testdb.NewDB(tb)
	require.NoError(tb, err)
	tb.Cleanup(func() { testdb.CloseDB(gormDB) })

	// Before seeding, so LogQueries:true does not echo every insert.
	counter := testdb.CountStatements(gormDB)
	hubID := seedHubWithBills(tb, gormDB, n)
	counter.Reset()

	return &appsService{db: gormDB}, counter, hubID
}

// BenchmarkGetCashHubStats is one dashboard load for one hub.
func BenchmarkGetCashHubStats(b *testing.B) {
	for _, n := range statsLadder {
		b.Run(fmt.Sprintf("bills=%d", n), func(b *testing.B) {
			svc, counter, hubID := benchStatsService(b, n)
			now := time.Now()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := svc.GetCashHubStats(hubID, now); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(counter.N())/float64(b.N), "stmts/op")
		})
	}
}

// BenchmarkGetAllCashHubStats is the operator-wide view, across every hub.
func BenchmarkGetAllCashHubStats(b *testing.B) {
	for _, n := range statsLadder {
		b.Run(fmt.Sprintf("bills=%d", n), func(b *testing.B) {
			svc, counter, _ := benchStatsService(b, n)
			now := time.Now()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := svc.GetAllCashHubStats(now); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(counter.N())/float64(b.N), "stmts/op")
		})
	}
}

// BenchmarkCashHubStatsParts breaks a dashboard load into its pieces, so the
// cost can be attributed rather than guessed at. Statement counts are already
// constant, so the question is which aggregate dominates.
func BenchmarkCashHubStatsParts(b *testing.B) {
	const n = 50_000

	parts := []struct {
		name string
		run  func(svc *appsService, stats *CashHubStats, scope cashStatsScope, now time.Time) error
	}{
		{"expiryBuckets", func(svc *appsService, stats *CashHubStats, scope cashStatsScope, now time.Time) error {
			return svc.fillCashExpiryBuckets(stats, scope, now)
		}},
		{"dailySeries", func(svc *appsService, stats *CashHubStats, scope cashStatsScope, now time.Time) error {
			return svc.fillCashDailySeries(stats, scope, now.AddDate(0, 0, -30), now)
		}},
		{"medianTimeToRedeem", func(svc *appsService, stats *CashHubStats, scope cashStatsScope, now time.Time) error {
			return svc.fillCashMedianTimeToRedeem(stats, scope)
		}},
		{"perHubOutstanding", func(svc *appsService, stats *CashHubStats, scope cashStatsScope, now time.Time) error {
			return svc.fillCashPerHubOutstanding(stats, now)
		}},
	}

	for _, part := range parts {
		b.Run(part.name, func(b *testing.B) {
			svc, counter, hubID := benchStatsService(b, n)
			now := time.Now()
			scope := forCashHub(hubID)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				stats := &CashHubStats{}
				if err := part.run(svc, stats, scope, now); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(counter.N())/float64(b.N), "stmts/op")
		})
	}
}
