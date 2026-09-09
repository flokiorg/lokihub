package migrations

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

func newDBForDropMaxTransfersTest(t *testing.T) *gorm.DB {
	t.Helper()
	uri := filepath.Join(t.TempDir(), "drop_cash_max_transfers_test.db")
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: uri})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })
	return gormDB
}

// createPreDropCashTables simulates a DB that still has max_transfers on
// both tables, from before this migration — the shape cash_hub_configs and
// cash_wallet_claims had right after MigrateRenameJITToCash, before this
// migration ever ran.
func createPreDropCashTables(t *testing.T, gormDB *gorm.DB) {
	t.Helper()
	require.NoError(t, gormDB.Exec(`CREATE TABLE apps (id INTEGER PRIMARY KEY AUTOINCREMENT)`).Error)
	require.NoError(t, gormDB.Exec(`CREATE TABLE cash_hub_configs (
		id                   INTEGER PRIMARY KEY AUTOINCREMENT,
		app_id               INTEGER NOT NULL UNIQUE,
		per_wallet_max_mloki INTEGER,
		max_exp_secs         INTEGER,
		min_transfer_mloki   INTEGER NOT NULL DEFAULT 0,
		max_transfers        INTEGER NOT NULL DEFAULT 0
	)`).Error)
	require.NoError(t, gormDB.Exec(`CREATE TABLE cash_wallet_claims (
		id                        INTEGER PRIMARY KEY AUTOINCREMENT,
		wallet_app_id             INTEGER NOT NULL,
		identity_type             TEXT NOT NULL,
		identity_value            TEXT NOT NULL,
		ia_pubkey                 TEXT,
		amount_mloki              INTEGER NOT NULL,
		claimed_at                DATETIME,
		max_transfers             INTEGER,
		transfer_count            INTEGER,
		min_transfer_mloki        INTEGER NOT NULL DEFAULT 0,
		spun_off_to_wallet_app_id INTEGER,
		created_at                DATETIME
	)`).Error)
}

func TestMigrateDropCashMaxTransfers_DropsBothColumns(t *testing.T) {
	gormDB := newDBForDropMaxTransfersTest(t)
	createPreDropCashTables(t, gormDB)

	require.NoError(t, MigrateDropCashMaxTransfers(gormDB))

	assert.False(t, gormDB.Migrator().HasColumn("cash_hub_configs", "max_transfers"))
	assert.False(t, gormDB.Migrator().HasColumn("cash_wallet_claims", "max_transfers"))
	// transfer_count is unrelated internal concurrency bookkeeping — must survive.
	assert.True(t, gormDB.Migrator().HasColumn("cash_wallet_claims", "transfer_count"))
}

func TestMigrateDropCashMaxTransfers_Idempotent(t *testing.T) {
	gormDB := newDBForDropMaxTransfersTest(t)
	createPreDropCashTables(t, gormDB)

	require.NoError(t, MigrateDropCashMaxTransfers(gormDB))
	require.NoError(t, MigrateDropCashMaxTransfers(gormDB), "running twice must be a no-op, not an error")

	assert.False(t, gormDB.Migrator().HasColumn("cash_hub_configs", "max_transfers"))
	assert.False(t, gormDB.Migrator().HasColumn("cash_wallet_claims", "max_transfers"))
}

func TestMigrateDropCashMaxTransfers_FreshDB_NoOp(t *testing.T) {
	gormDB := newDBForDropMaxTransfersTest(t)
	// No apps table at all — mirrors a brand-new install where AutoMigrate
	// creates every table straight from the current (already-columnless) structs.
	require.NoError(t, MigrateDropCashMaxTransfers(gormDB))
}
