package config

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/migrations"
	"github.com/flokiorg/lokihub/logger"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newMacaroonTestConfig(t *testing.T, appCfg *AppConfig) *config {
	t.Helper()
	logger.Init(strconv.Itoa(4))

	if appCfg.Workdir == "" {
		appCfg.Workdir = t.TempDir()
	}
	// A private on-disk DB per test: the package's other tests share one named
	// in-memory cache, and writing unlock/macaroon rows into it would leak
	// across tests.
	gdb, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "config-test.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.Migrate(gdb))
	cfg, err := NewConfig(appCfg, gdb)
	require.NoError(t, err)
	return cfg
}

func readRawRow(t *testing.T, cfg *config, key string) db.UserConfig {
	t.Helper()
	var row db.UserConfig
	require.NoError(t, cfg.db.Where(&db.UserConfig{Key: key}).Limit(1).Find(&row).Error)
	return row
}

// TestMacaroonEnvPath_EncryptedAtRest pins LOKIHUB-0031 on the env/docker path:
// init() must not persist a cleartext macaroon on boot, and the first unlock
// stores it encrypted, sourced from FLND_MACAROON_FILE.
func TestMacaroonEnvPath_EncryptedAtRest(t *testing.T) {
	macBytes := []byte("fake-admin-macaroon-bytes-0001")
	macHex := hex.EncodeToString(macBytes)

	dir := t.TempDir()
	macFile := filepath.Join(dir, "admin.macaroon")
	require.NoError(t, os.WriteFile(macFile, macBytes, 0600))

	cfg := newMacaroonTestConfig(t, &AppConfig{
		Workdir:          dir,
		FLNDAddress:      "flnd:10005",
		FLNDMacaroonFile: macFile,
	})

	// init() must NOT have persisted the macaroon before unlock.
	require.Empty(t, readRawRow(t, cfg, "LNDMacaroonHex").Key, "macaroon must not be persisted on boot before unlock")

	const pw = "unlock-pw"
	require.NoError(t, cfg.SaveUnlockPasswordCheck(pw))
	require.NoError(t, cfg.Unlock(pw))

	row := readRawRow(t, cfg, "LNDMacaroonHex")
	require.NotEmpty(t, row.Key)
	require.True(t, row.Encrypted, "macaroon row must be Encrypted=true")
	require.NotEqual(t, macHex, row.Value, "macaroon must not be stored in cleartext")

	got, err := cfg.Get("LNDMacaroonHex", pw)
	require.NoError(t, err)
	require.Equal(t, macHex, got, "encrypted macaroon must decrypt back to the original")
}

// TestMacaroonLegacyCleartext_MigratedOnUnlock pins the one-time migration: an
// existing cleartext row (written by an older version) is re-encrypted on the
// next unlock, without breaking an existing install.
func TestMacaroonLegacyCleartext_MigratedOnUnlock(t *testing.T) {
	macBytes := []byte("fake-admin-macaroon-bytes-0002")
	macHex := hex.EncodeToString(macBytes)

	cfg := newMacaroonTestConfig(t, &AppConfig{Workdir: t.TempDir()})

	// Simulate the legacy state: a cleartext macaroon row (Encrypted=false).
	require.NoError(t, cfg.SetUpdate("LNDMacaroonHex", macHex, ""))
	require.False(t, readRawRow(t, cfg, "LNDMacaroonHex").Encrypted)

	const pw = "unlock-pw"
	require.NoError(t, cfg.SaveUnlockPasswordCheck(pw))
	require.NoError(t, cfg.Unlock(pw))

	row := readRawRow(t, cfg, "LNDMacaroonHex")
	require.True(t, row.Encrypted, "legacy cleartext macaroon must be migrated to Encrypted=true")
	require.NotEqual(t, macHex, row.Value)

	got, err := cfg.Get("LNDMacaroonHex", pw)
	require.NoError(t, err)
	require.Equal(t, macHex, got)
}

// TestMacaroonEnvPath_NoRewriteWhenUnchanged confirms a second unlock does not
// rewrite an already-correct encrypted row.
func TestMacaroonEnvPath_NoRewriteWhenUnchanged(t *testing.T) {
	macBytes := []byte("fake-admin-macaroon-bytes-0003")
	dir := t.TempDir()
	macFile := filepath.Join(dir, "admin.macaroon")
	require.NoError(t, os.WriteFile(macFile, macBytes, 0600))

	cfg := newMacaroonTestConfig(t, &AppConfig{
		Workdir:          dir,
		FLNDMacaroonFile: macFile,
	})

	const pw = "unlock-pw"
	require.NoError(t, cfg.SaveUnlockPasswordCheck(pw))
	require.NoError(t, cfg.Unlock(pw))
	first := readRawRow(t, cfg, "LNDMacaroonHex").Value

	require.NoError(t, cfg.Unlock(pw))
	second := readRawRow(t, cfg, "LNDMacaroonHex").Value
	require.Equal(t, first, second, "an unchanged macaroon must not be rewritten on every unlock")
}
