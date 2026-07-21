package migrations

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

func newMigratedDBForRelayTest(t *testing.T) *gorm.DB {
	t.Helper()
	uri := filepath.Join(t.TempDir(), "replace_nostr_band_test.db")
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: uri})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })
	require.NoError(t, gormDB.AutoMigrate(&db.UserConfig{}))
	return gormDB
}

func getGeneralRelayValue(t *testing.T, gormDB *gorm.DB) string {
	t.Helper()
	var value string
	require.NoError(t, gormDB.Table("user_configs").Where("key = ?", "GeneralRelay").Pluck("value", &value).Error)
	return value
}

func TestMigrateReplaceNostrBandGeneralRelay_SwapsNostrBandForNosLol(t *testing.T) {
	gormDB := newMigratedDBForRelayTest(t)
	require.NoError(t, gormDB.Exec(`
		INSERT INTO user_configs (key, value, created_at, updated_at)
		VALUES ('GeneralRelay', 'wss://relay.damus.io,wss://relay.nostr.band,wss://relay.ohstr.com', datetime('now'), datetime('now'))
	`).Error)

	require.NoError(t, MigrateReplaceNostrBandGeneralRelay(gormDB))

	assert.Equal(t, "wss://relay.damus.io,wss://nos.lol,wss://relay.ohstr.com", getGeneralRelayValue(t, gormDB))
}

func TestMigrateReplaceNostrBandGeneralRelay_DedupesIfNosLolAlreadyPresent(t *testing.T) {
	gormDB := newMigratedDBForRelayTest(t)
	require.NoError(t, gormDB.Exec(`
		INSERT INTO user_configs (key, value, created_at, updated_at)
		VALUES ('GeneralRelay', 'wss://nos.lol,wss://relay.nostr.band,wss://relay.ohstr.com', datetime('now'), datetime('now'))
	`).Error)

	require.NoError(t, MigrateReplaceNostrBandGeneralRelay(gormDB))

	assert.Equal(t, "wss://nos.lol,wss://relay.ohstr.com", getGeneralRelayValue(t, gormDB))
}

func TestMigrateReplaceNostrBandGeneralRelay_NoNostrBand_Untouched(t *testing.T) {
	gormDB := newMigratedDBForRelayTest(t)
	require.NoError(t, gormDB.Exec(`
		INSERT INTO user_configs (key, value, created_at, updated_at)
		VALUES ('GeneralRelay', 'wss://relay.damus.io,wss://relay.primal.net', datetime('now'), datetime('now'))
	`).Error)

	require.NoError(t, MigrateReplaceNostrBandGeneralRelay(gormDB))

	assert.Equal(t, "wss://relay.damus.io,wss://relay.primal.net", getGeneralRelayValue(t, gormDB))
}

func TestMigrateReplaceNostrBandGeneralRelay_NoRowYet_NoOp(t *testing.T) {
	gormDB := newMigratedDBForRelayTest(t)
	// No GeneralRelay row inserted — mirrors a brand-new install where
	// config.Unlock seeds the new default directly, never touching this migration.
	require.NoError(t, MigrateReplaceNostrBandGeneralRelay(gormDB))
}

func TestMigrateReplaceNostrBandGeneralRelay_Idempotent(t *testing.T) {
	gormDB := newMigratedDBForRelayTest(t)
	require.NoError(t, gormDB.Exec(`
		INSERT INTO user_configs (key, value, created_at, updated_at)
		VALUES ('GeneralRelay', 'wss://relay.damus.io,wss://relay.nostr.band', datetime('now'), datetime('now'))
	`).Error)

	require.NoError(t, MigrateReplaceNostrBandGeneralRelay(gormDB))
	require.NoError(t, MigrateReplaceNostrBandGeneralRelay(gormDB), "running twice must be a no-op, not an error")

	assert.Equal(t, "wss://relay.damus.io,wss://nos.lol", getGeneralRelayValue(t, gormDB))
}
