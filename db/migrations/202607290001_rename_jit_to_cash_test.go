package migrations

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// newPreRenameDB builds a fresh sqlite DB with the pre-rename schema this
// migration expects to find on an existing (pre-Cash-Hub-redesign) install:
// apps/app_permissions (minimal columns) plus jit_hub_configs/
// jit_wallet_claims, populated with one hub, one wallet, and one recipient
// claim, mirroring what MigrateJITCircleConfigTables would have already
// produced on a real deployment.
func newPreRenameDB(t *testing.T) *gorm.DB {
	t.Helper()
	uri := filepath.Join(t.TempDir(), "rename_jit_to_cash_test.db")
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: uri})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })

	require.NoError(t, gormDB.Exec(`CREATE TABLE apps (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		name              TEXT,
		app_pubkey        TEXT NOT NULL,
		kind              TEXT NOT NULL DEFAULT 'standard',
		parent_app_id     INTEGER,
		parent_kind       TEXT,
		expires_at        DATETIME,
		cleanup_in_progress BOOLEAN NOT NULL DEFAULT false,
		created_at        DATETIME,
		updated_at        DATETIME
	)`).Error)
	require.NoError(t, gormDB.Exec(`CREATE TABLE app_permissions (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		app_id     INTEGER NOT NULL,
		scope      TEXT NOT NULL,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error)
	require.NoError(t, gormDB.Exec(`CREATE TABLE jit_hub_configs (
		id                   INTEGER PRIMARY KEY AUTOINCREMENT,
		app_id               INTEGER NOT NULL UNIQUE REFERENCES apps(id) ON DELETE CASCADE,
		per_wallet_max_mloki INTEGER,
		max_exp_secs         INTEGER
	)`).Error)
	require.NoError(t, gormDB.Exec(`CREATE TABLE jit_wallet_claims (
		id                        INTEGER PRIMARY KEY AUTOINCREMENT,
		wallet_app_id             INTEGER NOT NULL,
		identity_type             TEXT NOT NULL,
		identity_value            TEXT NOT NULL,
		ia_pubkey                 TEXT,
		amount_mloki              INTEGER NOT NULL,
		claimed_at                DATETIME,
		max_transfers             INTEGER,
		transfer_count            INTEGER,
		spun_off_to_wallet_app_id INTEGER,
		created_at                DATETIME
	)`).Error)
	require.NoError(t, gormDB.Exec(`
		CREATE UNIQUE INDEX idx_jit_claim_wallet_identity
		ON jit_wallet_claims(wallet_app_id, identity_type, identity_value)
	`).Error)

	// One hub, one wallet (child of that hub), one recipient claim on it —
	// exactly the shape a real pre-rename deployment would have.
	require.NoError(t, gormDB.Exec(`
		INSERT INTO apps (id, name, app_pubkey, kind, parent_app_id, parent_kind)
		VALUES (1, 'hub', 'hubpubkey', 'jit_hub', NULL, NULL)
	`).Error)
	require.NoError(t, gormDB.Exec(`
		INSERT INTO apps (id, name, app_pubkey, kind, parent_app_id, parent_kind)
		VALUES (2, 'wallet', 'walletpubkey', 'jit_wallet', 1, 'jit')
	`).Error)
	require.NoError(t, gormDB.Exec(`
		INSERT INTO app_permissions (id, app_id, scope) VALUES
			(1, 1, 'jit_hub'),
			(2, 2, 'jit_claim_funds'),
			(3, 2, 'jit_transfer'),
			(4, 2, 'get_balance')
	`).Error)
	require.NoError(t, gormDB.Exec(`
		INSERT INTO jit_hub_configs (app_id, per_wallet_max_mloki, max_exp_secs)
		VALUES (1, 100000, 3600)
	`).Error)
	require.NoError(t, gormDB.Exec(`
		INSERT INTO jit_wallet_claims (wallet_app_id, identity_type, identity_value, amount_mloki, max_transfers, transfer_count)
		VALUES (2, 'pubkey', 'deadbeef', 5000, 3, 0)
	`).Error)

	return gormDB
}

func TestMigrateRenameJITToCash_RenamesTablesAndAddsColumns(t *testing.T) {
	gormDB := newPreRenameDB(t)

	require.NoError(t, MigrateRenameJITToCash(gormDB))

	assert.True(t, gormDB.Migrator().HasTable("cash_hub_configs"))
	assert.True(t, gormDB.Migrator().HasTable("cash_wallet_claims"))
	assert.False(t, gormDB.Migrator().HasTable("jit_hub_configs"))
	assert.False(t, gormDB.Migrator().HasTable("jit_wallet_claims"))

	var hubConfig struct {
		PerWalletMaxMloki int
		MaxExpSecs        int
		MinTransferMloki  int64
		MaxTransfers      int
	}
	require.NoError(t, gormDB.Table("cash_hub_configs").
		Select("per_wallet_max_mloki, max_exp_secs, min_transfer_mloki, max_transfers").
		Where("app_id = ?", 1).Scan(&hubConfig).Error)
	assert.Equal(t, 100000, hubConfig.PerWalletMaxMloki)
	assert.Equal(t, 3600, hubConfig.MaxExpSecs)
	assert.Equal(t, int64(0), hubConfig.MinTransferMloki, "existing rows default to 0/no-floor — the feature didn't exist for them")
	assert.Equal(t, 0, hubConfig.MaxTransfers, "existing hubs default to 0/unlimited — matching what 'omitted' already meant on every mint_cash call before this became a hub setting")

	var claim struct {
		WalletAppID      uint
		IdentityType     string
		IdentityValue    string
		AmountMloki      int64
		MaxTransfers     int
		MinTransferMloki int64
	}
	require.NoError(t, gormDB.Table("cash_wallet_claims").
		Select("wallet_app_id, identity_type, identity_value, amount_mloki, max_transfers, min_transfer_mloki").
		Where("wallet_app_id = ?", 2).Scan(&claim).Error)
	assert.Equal(t, uint(2), claim.WalletAppID)
	assert.Equal(t, "pubkey", claim.IdentityType)
	assert.Equal(t, "deadbeef", claim.IdentityValue)
	assert.Equal(t, int64(5000), claim.AmountMloki)
	assert.Equal(t, 3, claim.MaxTransfers)
	assert.Equal(t, int64(0), claim.MinTransferMloki)
}

func TestMigrateRenameJITToCash_RenamesKindAndParentKindValues(t *testing.T) {
	gormDB := newPreRenameDB(t)
	require.NoError(t, MigrateRenameJITToCash(gormDB))

	var hubKind, walletKind, walletParentKind string
	require.NoError(t, gormDB.Table("apps").Where("id = ?", 1).Pluck("kind", &hubKind).Error)
	require.NoError(t, gormDB.Table("apps").Where("id = ?", 2).Pluck("kind", &walletKind).Error)
	require.NoError(t, gormDB.Table("apps").Where("id = ?", 2).Pluck("parent_kind", &walletParentKind).Error)
	assert.Equal(t, "cash_hub", hubKind)
	assert.Equal(t, "cash_wallet", walletKind)
	assert.Equal(t, "cash", walletParentKind)
}

func TestMigrateRenameJITToCash_RewritesGrantedScopes(t *testing.T) {
	gormDB := newPreRenameDB(t)
	require.NoError(t, MigrateRenameJITToCash(gormDB))

	var scopes []string
	require.NoError(t, gormDB.Table("app_permissions").Order("id asc").Pluck("scope", &scopes).Error)
	assert.Equal(t, []string{"cash_hub", "cash_redeem", "cash_transfer", "get_balance"}, scopes,
		"every existing granted permission must be rewritten so live connections don't break the instant this ships")
}

func TestMigrateRenameJITToCash_AddsSplitFromWalletAppIDColumn(t *testing.T) {
	gormDB := newPreRenameDB(t)
	require.NoError(t, MigrateRenameJITToCash(gormDB))
	assert.True(t, gormDB.Migrator().HasColumn("apps", "split_from_wallet_app_id"))
}

func TestMigrateRenameJITToCash_Idempotent(t *testing.T) {
	gormDB := newPreRenameDB(t)
	require.NoError(t, MigrateRenameJITToCash(gormDB))
	require.NoError(t, MigrateRenameJITToCash(gormDB), "running twice must be a no-op, not an error")

	var scopes []string
	require.NoError(t, gormDB.Table("app_permissions").Order("id asc").Pluck("scope", &scopes).Error)
	assert.Equal(t, []string{"cash_hub", "cash_redeem", "cash_transfer", "get_balance"}, scopes,
		"a second run must not re-rewrite already-renamed scopes into anything else")
}

// TestMigrateRenameJITToCash_SecondRunAfterUpgrade_AddsNewColumn is a
// regression test for a real bug caught in this same session: an earlier
// version of this migration checked HasTable using the OUTER db handle
// instead of the transaction's own tx handle inside db.Transaction, which
// silently orphaned the old jit_* tables instead of renaming them on a live
// deployment (fixed by using tx.Migrator() throughout).
//
// It originally also guarded a related risk: a DB that already went through
// an EARLIER version of this function (before max_transfers existed) had to
// still pick up a LATER version's new idempotent step (adding max_transfers)
// on its next run. That column has since been permanently removed by
// MigrateDropCashMaxTransfers, so the fast path no longer checks for it —
// its presence or absence can no longer distinguish "never migrated" from
// "migrated, including the now-retired column, which a later migration
// already dropped again." A DB in this exact shape (tables renamed,
// min_transfer_mloki present, max_transfers never added) now correctly short-
// circuits here without adding it — the end state (no max_transfers column)
// is identical either way, since MigrateDropCashMaxTransfers would remove it
// again immediately after if it were added.
func TestMigrateRenameJITToCash_SecondRunAfterUpgrade_SkipsRetiredColumn(t *testing.T) {
	gormDB := newPreRenameDB(t)

	// Simulate a DB that already went through a version of this migration
	// from before max_transfers existed: tables already renamed,
	// min_transfer_mloki already present, but max_transfers is not.
	require.NoError(t, gormDB.Exec(`ALTER TABLE jit_hub_configs RENAME TO cash_hub_configs`).Error)
	require.NoError(t, gormDB.Exec(`ALTER TABLE cash_hub_configs ADD COLUMN min_transfer_mloki INTEGER NOT NULL DEFAULT 0`).Error)
	require.NoError(t, gormDB.Exec(`ALTER TABLE jit_wallet_claims RENAME TO cash_wallet_claims`).Error)
	require.NoError(t, gormDB.Exec(`ALTER TABLE cash_wallet_claims ADD COLUMN min_transfer_mloki INTEGER NOT NULL DEFAULT 0`).Error)
	require.NoError(t, gormDB.Exec(`ALTER TABLE apps ADD COLUMN split_from_wallet_app_id INTEGER`).Error)
	require.NoError(t, gormDB.Exec(`UPDATE apps SET kind = 'cash_hub' WHERE kind = 'jit_hub'`).Error)
	require.NoError(t, gormDB.Exec(`UPDATE apps SET kind = 'cash_wallet' WHERE kind = 'jit_wallet'`).Error)
	require.NoError(t, gormDB.Exec(`UPDATE apps SET parent_kind = 'cash' WHERE parent_kind = 'jit'`).Error)
	require.NoError(t, gormDB.Exec(`UPDATE app_permissions SET scope = 'cash_hub' WHERE scope = 'jit_hub'`).Error)
	require.NoError(t, gormDB.Exec(`UPDATE app_permissions SET scope = 'cash_redeem' WHERE scope = 'jit_claim_funds'`).Error)
	require.NoError(t, gormDB.Exec(`UPDATE app_permissions SET scope = 'cash_transfer' WHERE scope = 'jit_transfer'`).Error)
	require.False(t, gormDB.Migrator().HasColumn("cash_hub_configs", "max_transfers"), "test setup sanity check")

	require.NoError(t, MigrateRenameJITToCash(gormDB))

	assert.False(t, gormDB.Migrator().HasColumn("cash_hub_configs", "max_transfers"),
		"the retired column must not be re-added — the fast path now short-circuits on min_transfer_mloki alone")

	// The pre-existing hub config data must be untouched by this second run.
	var hubConfig struct {
		PerWalletMaxMloki int
		MaxExpSecs        int
	}
	require.NoError(t, gormDB.Table("cash_hub_configs").
		Select("per_wallet_max_mloki, max_exp_secs").
		Where("app_id = ?", 1).Scan(&hubConfig).Error)
	assert.Equal(t, 100000, hubConfig.PerWalletMaxMloki)
	assert.Equal(t, 3600, hubConfig.MaxExpSecs)
}

func TestMigrateRenameJITToCash_FreshDB_NoOp(t *testing.T) {
	uri := filepath.Join(t.TempDir(), "rename_jit_to_cash_fresh_test.db")
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: uri})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })
	// No "apps" table at all yet — mirrors a brand-new install where
	// AutoMigrate creates everything (already under the new Cash names)
	// directly, never touching this migration.
	require.NoError(t, MigrateRenameJITToCash(gormDB))
	assert.False(t, gormDB.Migrator().HasTable("cash_hub_configs"))
}
