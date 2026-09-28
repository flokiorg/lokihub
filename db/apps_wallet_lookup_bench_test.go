package db_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	testdb "github.com/flokiorg/lokihub/tests/db"
)

// Measures the lookup the private transport performs once PER ITEM: resolve an app
// from a wallet pubkey.
//
// It needs its own index. The existing composite idx_apps_pubkey_lookup leads with
// app_pubkey, and a private-transport envelope has no app_pubkey to offer — its outer
// key is a fresh ephemeral one — so `WHERE wallet_pubkey = ?` cannot use that index at
// all. Without a standalone index it is a full scan of apps, per item, on a table that
// grows with every split (two new wallets) and every consolidate (one).
//
// Run: go test ./db/ -run XXX -bench BenchmarkAppByWalletPubkey -benchmem
const benchApps = 50_000

func seedApps(b *testing.B) (*gorm.DB, string) {
	b.Helper()
	gormDB, err := testdb.NewDB(b)
	require.NoError(b, err)

	// Raw INSERT from a recursive CTE: 50k rows through gorm's logger would dominate
	// the setup and tell us nothing.
	require.NoError(b, gormDB.Exec(fmt.Sprintf(`
		INSERT INTO apps (name, description, app_pubkey, wallet_pubkey, kind, parent_kind, created_at, updated_at)
		SELECT 'bench-'||n, '', printf('%%064x', n), printf('%%064x', n+1000000), 'cash_wallet', 'cash',
		       datetime('now'), datetime('now')
		FROM (WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM s WHERE n < %d) SELECT n FROM s)`,
		benchApps)).Error)

	var count int64
	require.NoError(b, gormDB.Model(&db.App{}).Count(&count).Error)
	require.Equal(b, int64(benchApps), count, "fixture size")

	// A pubkey in the middle, so neither end of the table is favoured.
	target := fmt.Sprintf("%064x", benchApps/2+1000000)
	return gormDB, target
}

func lookup(b *testing.B, gormDB *gorm.DB, target string) {
	b.Helper()
	for i := 0; i < b.N; i++ {
		var app db.App
		if err := gormDB.Where("wallet_pubkey = ?", target).First(&app).Error; err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAppByWalletPubkey_WithIndex(b *testing.B) {
	gormDB, target := seedApps(b)
	b.ResetTimer()
	lookup(b, gormDB, target)
}

// WithoutIndex drops the standalone index to measure what the private transport would
// pay per item without it. The composite index remains, which is the point: it cannot
// serve this query, so what is left is a scan.
func BenchmarkAppByWalletPubkey_WithoutIndex(b *testing.B) {
	gormDB, target := seedApps(b)
	require.NoError(b, gormDB.Exec(`DROP INDEX IF EXISTS idx_apps_wallet_pubkey`).Error)
	b.ResetTimer()
	lookup(b, gormDB, target)
}
