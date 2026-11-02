package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewDB_RestrictsSqliteFilePerms pins LOKIHUB-0036: the sqlite database and
// its WAL/SHM sidecars must be owner-only (0600) rather than left at the umask
// default, because the DB holds key material.
func TestNewDB_RestrictsSqliteFilePerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nwc.db")

	gormDB, err := NewDB(path, false)
	require.NoError(t, err)

	// Force a write so the file (and, under WAL, the -wal/-shm sidecars) exist.
	require.NoError(t, gormDB.Exec("CREATE TABLE perm_probe (id INTEGER)").Error)
	require.NoError(t, gormDB.Exec("INSERT INTO perm_probe (id) VALUES (1)").Error)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm(), "nwc.db must be 0600")

	// Any sidecar that exists must also be owner-only.
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		sidecar := path + suffix
		si, err := os.Stat(sidecar)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), si.Mode().Perm(), "%s must be 0600", sidecar)
	}
}
