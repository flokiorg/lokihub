package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAutoUnlockPassword_EncryptedAtRest pins LOKIHUB-0033: the auto-unlock
// password (the KEK) must not be stored in cleartext in nwc.db.
func TestAutoUnlockPassword_EncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	cfg := newMacaroonTestConfig(t, &AppConfig{Workdir: dir})

	const pw = "unlock-pw"
	require.NoError(t, cfg.SaveUnlockPasswordCheck(pw))

	require.NoError(t, cfg.SetAutoUnlockPassword(pw))

	row := readRawRow(t, cfg, "AutoUnlockPassword")
	require.NotEmpty(t, row.Value)
	require.NotEqual(t, pw, row.Value, "auto-unlock password must not be stored in cleartext")
	require.True(t, strings.HasPrefix(row.Value, autoUnlockEncPrefix), "stored value must be marked encrypted")

	got, err := cfg.GetAutoUnlockPassword()
	require.NoError(t, err)
	require.Equal(t, pw, got)

	// The host key lives outside the DB, in a 0600 file in the workdir.
	info, err := os.Stat(filepath.Join(dir, autoUnlockKeyFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

// TestAutoUnlockPassword_Disable confirms disabling stores an empty value so
// presence checks (backup/password-change refusals, UI state) still see "".
func TestAutoUnlockPassword_Disable(t *testing.T) {
	cfg := newMacaroonTestConfig(t, &AppConfig{Workdir: t.TempDir()})

	const pw = "unlock-pw"
	require.NoError(t, cfg.SaveUnlockPasswordCheck(pw))
	require.NoError(t, cfg.SetAutoUnlockPassword(pw))
	require.NoError(t, cfg.SetAutoUnlockPassword(""))

	require.Empty(t, readRawRow(t, cfg, "AutoUnlockPassword").Value)
	got, err := cfg.GetAutoUnlockPassword()
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestAutoUnlockPassword_LegacyMigration confirms a pre-existing cleartext
// value is returned correctly and migrated to encrypted on read.
func TestAutoUnlockPassword_LegacyMigration(t *testing.T) {
	cfg := newMacaroonTestConfig(t, &AppConfig{Workdir: t.TempDir()})

	const pw = "unlock-pw"
	// Simulate the legacy state: a cleartext auto-unlock password.
	require.NoError(t, cfg.SetUpdate("AutoUnlockPassword", pw, ""))
	require.Equal(t, pw, readRawRow(t, cfg, "AutoUnlockPassword").Value)

	got, err := cfg.GetAutoUnlockPassword()
	require.NoError(t, err)
	require.Equal(t, pw, got)

	// The read migrated it to an encrypted value in place.
	row := readRawRow(t, cfg, "AutoUnlockPassword")
	require.True(t, strings.HasPrefix(row.Value, autoUnlockEncPrefix), "cleartext value must be migrated to encrypted on read")
	require.NotEqual(t, pw, row.Value)
}
