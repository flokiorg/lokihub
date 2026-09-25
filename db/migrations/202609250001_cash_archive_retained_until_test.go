package migrations

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// newDBForRetainedUntilTest gives each test a fully migrated database, so the
// backfill runs against the real schema rather than a hand-built one.
func newDBForRetainedUntilTest(t *testing.T) *gorm.DB {
	t.Helper()
	uri := filepath.Join(t.TempDir(), "cash_archive_retained_until_test.db")
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: uri})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })
	require.NoError(t, Migrate(gormDB))
	return gormDB
}

// newHubApp inserts a Cash Hub app and returns its id, since
// cash_hub_configs.app_id carries a foreign key to it.
func newHubApp(t *testing.T, gormDB *gorm.DB, name string) uint {
	t.Helper()
	app := db.App{Name: name, AppPubkey: name + "-pubkey", Kind: db.AppKindCashHub}
	require.NoError(t, gormDB.Create(&app).Error)
	return app.ID
}

// TestMigrateCashArchiveRetainedUntil_Backfill checks the value the migration
// computes against the arithmetic it replaces: ended_at + spent_retention_secs,
// which SpentBillRetainedUntil used to do in Go after a join.
//
// It also pins the two NULL cases, both of which already meant "no tombstone":
// a Hub with retention disabled, and a Hub whose config row is gone.
func TestMigrateCashArchiveRetainedUntil_Backfill(t *testing.T) {
	gormDB := newDBForRetainedUntilTest(t)

	endedAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	// Hub A retains for a day; hub B has retention off; hub C has no config.
	// cash_hub_configs.app_id is a foreign key, so the apps must exist.
	hubA := newHubApp(t, gormDB, "hub-a")
	hubB := newHubApp(t, gormDB, "hub-b")
	hubC := newHubApp(t, gormDB, "hub-c")
	require.NoError(t, gormDB.Create(&db.CashHubConfig{AppID: hubA, SpentRetentionSecs: 86_400}).Error)
	require.NoError(t, gormDB.Create(&db.CashHubConfig{AppID: hubB, SpentRetentionSecs: 0}).Error)

	rows := []db.CashBillArchive{
		{WalletAppID: 1, HubAppID: hubA, WalletPubkey: "aa", EndedAt: endedAt, Outcome: db.CashBillOutcomeExpired},
		{WalletAppID: 2, HubAppID: hubB, WalletPubkey: "bb", EndedAt: endedAt, Outcome: db.CashBillOutcomeExpired},
		{WalletAppID: 3, HubAppID: hubC, WalletPubkey: "cc", EndedAt: endedAt, Outcome: db.CashBillOutcomeExpired},
	}
	require.NoError(t, gormDB.Create(&rows).Error)
	// Clear what AutoMigrate-era code may have set, so the migration is what
	// fills these in.
	require.NoError(t, gormDB.Model(&db.CashBillArchive{}).
		Where("1 = 1").Update("retained_until", nil).Error)

	require.NoError(t, MigrateCashArchiveRetainedUntil(gormDB))

	get := func(pubkey string) *time.Time {
		var row db.CashBillArchive
		require.NoError(t, gormDB.Where("wallet_pubkey = ?", pubkey).First(&row).Error)
		return row.RetainedUntil
	}

	retained := get("aa")
	require.NotNil(t, retained, "a hub with retention on must get a deadline")
	assert.WithinDuration(t, endedAt.Add(24*time.Hour), retained.UTC(), time.Second,
		"the deadline must be ended_at + spent_retention_secs")

	assert.Nil(t, get("bb"), "retention disabled means no tombstone, so NULL")
	assert.Nil(t, get("cc"), "a hub with no config means no tombstone, so NULL")
}

// TestMigrateCashArchiveRetainedUntil_Idempotent matters because migrations run
// on every start: a second pass must not move a deadline already set, or a bill
// would silently gain retention every restart.
func TestMigrateCashArchiveRetainedUntil_Idempotent(t *testing.T) {
	gormDB := newDBForRetainedUntilTest(t)

	endedAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	hub := newHubApp(t, gormDB, "hub-idempotent")
	require.NoError(t, gormDB.Create(&db.CashHubConfig{AppID: hub, SpentRetentionSecs: 3_600}).Error)
	require.NoError(t, gormDB.Create(&db.CashBillArchive{
		WalletAppID: 11, HubAppID: hub, WalletPubkey: "dd", EndedAt: endedAt, Outcome: db.CashBillOutcomeExpired,
	}).Error)
	require.NoError(t, gormDB.Model(&db.CashBillArchive{}).
		Where("wallet_pubkey = ?", "dd").Update("retained_until", nil).Error)

	require.NoError(t, MigrateCashArchiveRetainedUntil(gormDB))
	var first db.CashBillArchive
	require.NoError(t, gormDB.Where("wallet_pubkey = ?", "dd").First(&first).Error)
	require.NotNil(t, first.RetainedUntil)

	require.NoError(t, MigrateCashArchiveRetainedUntil(gormDB))
	var second db.CashBillArchive
	require.NoError(t, gormDB.Where("wallet_pubkey = ?", "dd").First(&second).Error)
	require.NotNil(t, second.RetainedUntil)

	assert.WithinDuration(t, *first.RetainedUntil, *second.RetainedUntil, 0,
		"a second migration pass must leave an existing deadline alone")
}
