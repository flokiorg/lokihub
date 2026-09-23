package migrations

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

func newDBForRenameBearerTest(t *testing.T) *gorm.DB {
	t.Helper()
	uri := filepath.Join(t.TempDir(), "rename_bearer_identity_to_cash_test.db")
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: uri})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })
	return gormDB
}

// createPreRenameCashClaims simulates a DB from before the bearer→cash
// rename: cash_wallet_claims carrying the old "bearer" identity_type,
// alongside the two identity-bound types the rename doesn't touch.
func createPreRenameCashClaims(t *testing.T, gormDB *gorm.DB) {
	t.Helper()
	require.NoError(t, gormDB.Exec(`CREATE TABLE apps (id INTEGER PRIMARY KEY AUTOINCREMENT)`).Error)
	require.NoError(t, gormDB.Exec(`CREATE TABLE cash_wallet_claims (
		id                        INTEGER PRIMARY KEY AUTOINCREMENT,
		wallet_app_id             INTEGER NOT NULL,
		identity_type             TEXT NOT NULL,
		identity_value            TEXT NOT NULL,
		ia_pubkey                 TEXT,
		amount_mloki              INTEGER NOT NULL,
		claimed_at                DATETIME,
		transfer_count            INTEGER,
		min_transfer_mloki        INTEGER NOT NULL DEFAULT 0,
		spun_off_to_wallet_app_id INTEGER,
		created_at                DATETIME
	)`).Error)
	require.NoError(t, gormDB.Exec(`CREATE UNIQUE INDEX idx_cash_claim_wallet_identity
		ON cash_wallet_claims(wallet_app_id, identity_type, identity_value)`).Error)

	// Two bearer slices on different wallets, plus one pubkey and one
	// connection_key slice that must come through untouched.
	require.NoError(t, gormDB.Exec(`INSERT INTO cash_wallet_claims
		(wallet_app_id, identity_type, identity_value, ia_pubkey, amount_mloki, transfer_count)
		VALUES
		(1, 'bearer', 'aa11', '', 1000, 0),
		(2, 'bearer', 'bb22', '', 2000, 3),
		(3, 'pubkey', 'cc33', '', 3000, 0),
		(4, 'connection_key', 'dd44', 'ia55', 4000, 0)`).Error)
}

func identityTypeOf(t *testing.T, gormDB *gorm.DB, walletAppID uint) string {
	t.Helper()
	var got string
	require.NoError(t, gormDB.Raw(
		`SELECT identity_type FROM cash_wallet_claims WHERE wallet_app_id = ?`, walletAppID,
	).Scan(&got).Error)
	return got
}

func TestMigrateRenameBearerIdentityToCash_RenamesOnlyBearerRows(t *testing.T) {
	gormDB := newDBForRenameBearerTest(t)
	createPreRenameCashClaims(t, gormDB)

	require.NoError(t, MigrateRenameBearerIdentityToCash(gormDB))

	assert.Equal(t, db.CashIdentityCash, identityTypeOf(t, gormDB, 1))
	assert.Equal(t, db.CashIdentityCash, identityTypeOf(t, gormDB, 2))
	assert.Equal(t, db.CashIdentityPubkey, identityTypeOf(t, gormDB, 3))
	assert.Equal(t, db.CashIdentityConnectionKey, identityTypeOf(t, gormDB, 4))

	var leftovers int64
	require.NoError(t, gormDB.Raw(
		`SELECT COUNT(*) FROM cash_wallet_claims WHERE identity_type = 'bearer'`,
	).Scan(&leftovers).Error)
	assert.Zero(t, leftovers, "no row may keep the old spelling")
}

// TestMigrateRenameBearerIdentityToCash_PreservesEverythingElse guards the
// property holders depend on: identity_value is the sha256 commitment of a
// cash-mode slice's secret, so the rename must leave it (and the amount,
// and the transfer counter the concurrency guards pin against) alone.
func TestMigrateRenameBearerIdentityToCash_PreservesEverythingElse(t *testing.T) {
	gormDB := newDBForRenameBearerTest(t)
	createPreRenameCashClaims(t, gormDB)

	require.NoError(t, MigrateRenameBearerIdentityToCash(gormDB))

	var row struct {
		IdentityValue string
		AmountMloki   int64
		TransferCount int64
	}
	require.NoError(t, gormDB.Raw(
		`SELECT identity_value, amount_mloki, transfer_count FROM cash_wallet_claims WHERE wallet_app_id = 2`,
	).Scan(&row).Error)

	assert.Equal(t, "bb22", row.IdentityValue)
	assert.EqualValues(t, 2000, row.AmountMloki)
	assert.EqualValues(t, 3, row.TransferCount)
}

func TestMigrateRenameBearerIdentityToCash_Idempotent(t *testing.T) {
	gormDB := newDBForRenameBearerTest(t)
	createPreRenameCashClaims(t, gormDB)

	require.NoError(t, MigrateRenameBearerIdentityToCash(gormDB))
	require.NoError(t, MigrateRenameBearerIdentityToCash(gormDB), "a second run must be a no-op")

	assert.Equal(t, db.CashIdentityCash, identityTypeOf(t, gormDB, 1))
}

func TestMigrateRenameBearerIdentityToCash_NoClaimsTable(t *testing.T) {
	gormDB := newDBForRenameBearerTest(t)

	assert.NoError(t, MigrateRenameBearerIdentityToCash(gormDB), "a fresh DB has nothing to rewrite")
}

// TestMigrateRenameBearerIdentityToCash_UniqueIndexSurvives documents why
// the rewrite can't trip idx_cash_claim_wallet_identity: no pre-rename code
// path ever wrote "cash", so the name this migration moves each row to is
// always free. A row that would collide is therefore impossible in practice
// — but if one ever were, the transaction fails rather than silently
// dropping a slice.
func TestMigrateRenameBearerIdentityToCash_UniqueIndexSurvives(t *testing.T) {
	gormDB := newDBForRenameBearerTest(t)
	createPreRenameCashClaims(t, gormDB)

	require.NoError(t, MigrateRenameBearerIdentityToCash(gormDB))

	err := gormDB.Exec(`INSERT INTO cash_wallet_claims
		(wallet_app_id, identity_type, identity_value, ia_pubkey, amount_mloki, transfer_count)
		VALUES (1, 'cash', 'aa11', '', 1000, 0)`).Error
	assert.Error(t, err, "the unique index must still reject a duplicate slice")
}
