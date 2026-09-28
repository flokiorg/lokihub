package db_test

// Measures the cost the A-1 fix ADDS to cash_transfer's full-to-cash path.
//
// The check today is `len(ListClaimsForWallet(id)) != 1`
// (cash_transfer_controller.go:472) — live claim rows only, which is why a bill
// that always had two recipients reads 1 after an admin deleted one. The fix
// needs the lifetime count: live rows plus the rows DeleteCashClaim archived.
//
// The live-claims query is already paid today and is unchanged, so what matters
// is only the archive lookup the fix introduces. Two shapes:
//
//	count   COUNT(*) on cash_bill_slice_archives by wallet_app_id
//	exists  SELECT 1 ... LIMIT 1 on the same index
//
// "exists" is the one to ship. The archive is consulted ONLY when the live count
// is exactly 1, and in that case the sole question is whether an archived sibling
// exists at all — never how many. So the added work is an index seek that stops
// at the first match, and it is skipped entirely for a wallet already
// disqualified by its live rows.
//
// Seeded via a recursive CTE rather than gorm: CashBillSliceArchive carries no FK
// association, so rows insert freely, and one statement avoids 200k logged
// INSERTs.
//
// Run: go test ./db/ -run XXX -bench BenchmarkArchiveLookup -benchmem

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	testdb "github.com/flokiorg/lokihub/tests/db"
)

const (
	benchWallets     = 20_000 // distinct wallet_app_id values
	benchArchivedPer = 10     // → 200k archive rows
	benchTarget      = benchWallets / 2
)

func seedArchive(b *testing.B) *gorm.DB {
	b.Helper()
	gormDB, err := testdb.NewDB(b)
	require.NoError(b, err)

	require.NoError(b, gormDB.Exec(fmt.Sprintf(`
		INSERT INTO cash_bill_slice_archives
			(wallet_app_id, hub_app_id, claim_id, identity_type, identity_value,
			 amount_mloki, outcome, created_at, archived_at)
		SELECT w, 1, w*100+s, 'pubkey', printf('%%064x', w*100+s), 1000,
		       'reclaimed', datetime('now'), datetime('now')
		FROM (WITH RECURSIVE ws(w) AS (
		        SELECT 1 UNION ALL SELECT w+1 FROM ws WHERE w < %d) SELECT w FROM ws),
		     (WITH RECURSIVE ss(s) AS (
		        SELECT 0 UNION ALL SELECT s+1 FROM ss WHERE s < %d) SELECT s FROM ss)`,
		benchWallets, benchArchivedPer-1)).Error)

	var total int64
	require.NoError(b, gormDB.Raw(`SELECT COUNT(*) FROM cash_bill_slice_archives`).Scan(&total).Error)
	require.Equal(b, int64(benchWallets*benchArchivedPer), total, "fixture size")
	return gormDB
}

func BenchmarkArchiveLookup_Count(b *testing.B) {
	gormDB := seedArchive(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var n int64
		if err := gormDB.Raw(
			`SELECT COUNT(*) FROM cash_bill_slice_archives WHERE wallet_app_id = ?`,
			benchTarget).Scan(&n).Error; err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkArchiveLookup_Exists(b *testing.B) {
	gormDB := seedArchive(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var one int64
		if err := gormDB.Raw(
			`SELECT 1 FROM cash_bill_slice_archives WHERE wallet_app_id = ? LIMIT 1`,
			benchTarget).Scan(&one).Error; err != nil {
			b.Fatal(err)
		}
	}
}

// Absent: the wallet has no archived sibling — the common case for a genuinely
// lifetime-solo bill, and the one that must stay cheap since it is the path that
// still reassigns in place.
func BenchmarkArchiveLookup_Exists_Absent(b *testing.B) {
	gormDB := seedArchive(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var one int64
		if err := gormDB.Raw(
			`SELECT 1 FROM cash_bill_slice_archives WHERE wallet_app_id = ? LIMIT 1`,
			benchWallets+1).Scan(&one).Error; err != nil {
			b.Fatal(err)
		}
	}
}

// Baseline: a row-materialising SELECT by the same indexed column, standing in
// for ListClaimsForWallet — which cannot be benchmarked here without seeding
// 20k FK'd App rows, and which does structurally identical work (index seek plus
// struct scan). This is what the path already pays today, so it is the number
// the added lookup should be read against.
func BenchmarkArchiveLookup_BaselineRowFetch(b *testing.B) {
	gormDB := seedArchive(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var rows []struct {
			WalletAppID uint
			AmountMloki int64
			Outcome     string
		}
		if err := gormDB.Raw(
			`SELECT wallet_app_id, amount_mloki, outcome FROM cash_bill_slice_archives WHERE wallet_app_id = ?`,
			benchTarget).Scan(&rows).Error; err != nil {
			b.Fatal(err)
		}
	}
}
