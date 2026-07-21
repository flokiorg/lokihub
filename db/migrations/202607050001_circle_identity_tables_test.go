package migrations

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// newPreMigrationDB opens a bare SQLite DB (no AutoMigrate, no other manual
// migrations) with just the old-shape tables this migration touches, so the
// backfill/upgrade path can be tested in isolation from a genuinely
// pre-migration state rather than a fresh DB (which would just take the
// early-return guard and never exercise the backfill logic).
func newPreMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	uri := filepath.Join(t.TempDir(), "circle_identity_migration_test.db")
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: uri})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })

	require.NoError(t, gormDB.Exec(`CREATE TABLE apps (
		id   INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		kind TEXT NOT NULL DEFAULT 'standard'
	)`).Error)

	require.NoError(t, gormDB.Exec(`CREATE TABLE circle_hub_configs (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		app_id          INTEGER NOT NULL UNIQUE REFERENCES apps(id) ON DELETE CASCADE,
		policy          TEXT,
		provider_pubkey TEXT,
		max_exp_secs    INTEGER,
		fees_ppm        INTEGER
	)`).Error)

	require.NoError(t, gormDB.Exec(`CREATE TABLE circle_allowed_pubkeys (
		id     INTEGER PRIMARY KEY AUTOINCREMENT,
		app_id INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		pubkey TEXT NOT NULL
	)`).Error)

	return gormDB
}

func TestMigrateCircleIdentityTables_BackfillsPerAppIdentity(t *testing.T) {
	gormDB := newPreMigrationDB(t)

	// Two pre-existing circle_hub apps, one following, one allowlist —
	// including a NULL/empty provider_pubkey on the allowlist one, which must
	// not make the migration choke.
	require.NoError(t, gormDB.Exec(`INSERT INTO apps (id, name, kind) VALUES (1, 'circle-a', 'circle_hub')`).Error)
	require.NoError(t, gormDB.Exec(`INSERT INTO apps (id, name, kind) VALUES (2, 'circle-b', 'circle_hub')`).Error)
	require.NoError(t, gormDB.Exec(`
		INSERT INTO circle_hub_configs (app_id, policy, provider_pubkey, max_exp_secs, fees_ppm)
		VALUES (1, 'following', 'deadbeef', 3600, 100)
	`).Error)
	require.NoError(t, gormDB.Exec(`
		INSERT INTO circle_hub_configs (app_id, policy, provider_pubkey, max_exp_secs, fees_ppm)
		VALUES (2, 'allowlist', '', 7200, 0)
	`).Error)
	require.NoError(t, gormDB.Exec(`INSERT INTO circle_allowed_pubkeys (app_id, pubkey) VALUES (2, 'aaaa1111')`).Error)
	require.NoError(t, gormDB.Exec(`INSERT INTO circle_allowed_pubkeys (app_id, pubkey) VALUES (2, 'bbbb2222')`).Error)

	require.NoError(t, MigrateCircleIdentityTables(gormDB))

	// Old columns/table are gone.
	var policyColCount, pubkeyColCount int
	require.NoError(t, gormDB.Raw(`SELECT COUNT(*) FROM pragma_table_info('circle_hub_configs') WHERE name='policy'`).Scan(&policyColCount).Error)
	require.NoError(t, gormDB.Raw(`SELECT COUNT(*) FROM pragma_table_info('circle_hub_configs') WHERE name='provider_pubkey'`).Scan(&pubkeyColCount).Error)
	assert.Equal(t, 0, policyColCount)
	assert.Equal(t, 0, pubkeyColCount)
	assert.False(t, gormDB.Migrator().HasTable("circle_allowed_pubkeys"))

	// Each app_id correlates back to its OWN identity, not just "some" identity.
	type cfgRow struct {
		AppID            uint
		CircleIdentityID uint
	}
	var cfgs []cfgRow
	require.NoError(t, gormDB.Raw(`SELECT app_id, circle_identity_id FROM circle_hub_configs ORDER BY app_id`).Scan(&cfgs).Error)
	require.Len(t, cfgs, 2)
	assert.NotEqual(t, cfgs[0].CircleIdentityID, cfgs[1].CircleIdentityID, "each app must get its own identity, not share one by accident")

	type identityRow struct {
		ID             uint
		Name           string
		Policy         string
		ProviderPubkey string
	}
	identityByID := map[uint]identityRow{}
	var identities []identityRow
	require.NoError(t, gormDB.Raw(`SELECT id, name, policy, provider_pubkey FROM circle_identities`).Scan(&identities).Error)
	require.Len(t, identities, 2)
	for _, i := range identities {
		identityByID[i.ID] = i
	}

	followingIdentity := identityByID[cfgs[0].CircleIdentityID]
	assert.Equal(t, "circle-a", followingIdentity.Name)
	assert.Equal(t, "following", followingIdentity.Policy)
	assert.Equal(t, "deadbeef", followingIdentity.ProviderPubkey)

	allowlistIdentity := identityByID[cfgs[1].CircleIdentityID]
	assert.Equal(t, "circle-b", allowlistIdentity.Name)
	assert.Equal(t, "allowlist", allowlistIdentity.Policy)

	// The old allowlist rows migrated to the new identity-scoped table,
	// correctly resolved through circle-b's identity.
	var allowedPubkeys []string
	require.NoError(t, gormDB.Raw(`SELECT pubkey FROM circle_identity_allowed_pubkeys WHERE circle_identity_id = ? ORDER BY pubkey`,
		cfgs[1].CircleIdentityID).Scan(&allowedPubkeys).Error)
	assert.Equal(t, []string{"aaaa1111", "bbbb2222"}, allowedPubkeys)
}

func TestMigrateCircleIdentityTables_Idempotent(t *testing.T) {
	gormDB := newPreMigrationDB(t)
	require.NoError(t, gormDB.Exec(`INSERT INTO apps (id, name, kind) VALUES (1, 'circle-a', 'circle_hub')`).Error)
	require.NoError(t, gormDB.Exec(`
		INSERT INTO circle_hub_configs (app_id, policy, provider_pubkey) VALUES (1, 'following', 'deadbeef')
	`).Error)

	require.NoError(t, MigrateCircleIdentityTables(gormDB))
	require.NoError(t, MigrateCircleIdentityTables(gormDB), "running the migration twice must be a no-op, not an error")

	var count int64
	require.NoError(t, gormDB.Table("circle_identities").Count(&count).Error)
	assert.Equal(t, int64(1), count, "the second run must not duplicate identities")
}

func TestMigrateCircleIdentityTables_FreshDB_NoOp(t *testing.T) {
	uri := filepath.Join(t.TempDir(), "fresh.db")
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: uri})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })

	// No apps/circle_hub_configs table at all yet.
	require.NoError(t, MigrateCircleIdentityTables(gormDB), "must be a no-op on a DB with no circle_hub_configs table")
}
