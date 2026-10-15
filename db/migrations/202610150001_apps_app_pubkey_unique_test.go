package migrations

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

func newDBWithApps(t *testing.T, pubkeys ...string) *gorm.DB {
	t.Helper()
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: filepath.Join(t.TempDir(), "app_pubkey_unique_test.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })
	require.NoError(t, gormDB.AutoMigrate(&db.App{}))
	for i, pk := range pubkeys {
		require.NoError(t, gormDB.Create(&db.App{Name: "app " + string(rune('a'+i)), AppPubkey: pk, Kind: db.AppKindIsolated}).Error)
	}
	return gormDB
}

func hasAppPubkeyUniqueIndex(t *testing.T, gormDB *gorm.DB) bool {
	t.Helper()
	return gormDB.Migrator().HasIndex(&db.App{}, "idx_apps_app_pubkey_unique")
}

func TestMigrateAppsAppPubkeyUnique_ExistingApps(t *testing.T) {
	gormDB := newDBWithApps(t, "aa", "bb", "cc")

	require.NoError(t, MigrateAppsAppPubkeyUnique(gormDB))
	require.NoError(t, MigrateAppsAppPubkeyUnique(gormDB), "a second run is a no-op")
	require.True(t, hasAppPubkeyUniqueIndex(t, gormDB))

	var count int64
	require.NoError(t, gormDB.Model(&db.App{}).Count(&count).Error)
	require.EqualValues(t, 3, count, "existing apps are untouched")

	err := gormDB.Create(&db.App{Name: "dup", AppPubkey: "bb", Kind: db.AppKindIsolated}).Error
	require.True(t, db.IsDuplicateKeyError(err), "a duplicate is refused, got %v", err)
	require.NoError(t, gormDB.Create(&db.App{Name: "new", AppPubkey: "dd", Kind: db.AppKindIsolated}).Error)
}

// A database that already holds duplicates can't take the index; the migration
// must not fail startup over it.
func TestMigrateAppsAppPubkeyUnique_LegacyDuplicates_SkippedNotFatal(t *testing.T) {
	gormDB := newDBWithApps(t, "aa", "aa", "bb")

	require.NoError(t, MigrateAppsAppPubkeyUnique(gormDB))
	require.False(t, hasAppPubkeyUniqueIndex(t, gormDB))

	var count int64
	require.NoError(t, gormDB.Model(&db.App{}).Count(&count).Error)
	require.EqualValues(t, 3, count, "nothing is deleted")
}

func TestMigrateAppsAppPubkeyUnique_NoAppsTable(t *testing.T) {
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: filepath.Join(t.TempDir(), "empty.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })
	require.NoError(t, MigrateAppsAppPubkeyUnique(gormDB))
}

// The full migration of a fresh database ends with the index.
func TestMigrate_FreshDatabase_HasAppPubkeyUniqueIndex(t *testing.T) {
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: filepath.Join(t.TempDir(), "fresh.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })
	require.NoError(t, Migrate(gormDB))
	require.True(t, hasAppPubkeyUniqueIndex(t, gormDB))
}
