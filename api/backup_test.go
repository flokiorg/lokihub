package api

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/pbkdf2"

	"github.com/flokiorg/lokihub/tests"
)

func TestBackupEncryptDecrypt_RoundTrip(t *testing.T) {
	password := "test-password"
	plaintext := []byte("this is a fake zip archive of a lokihub backup")

	var buf bytes.Buffer
	cw, err := encryptingWriter(&buf, password)
	require.NoError(t, err)
	_, err = cw.Write(plaintext)
	require.NoError(t, err)

	cr, err := decryptingReader(&buf, password)
	require.NoError(t, err)
	decrypted, err := io.ReadAll(cr)
	require.NoError(t, err)

	assert.Equal(t, plaintext, decrypted)
}

func TestBackupEncryptDecrypt_WrongPassword(t *testing.T) {
	plaintext := []byte("this is a fake zip archive of a lokihub backup")

	var buf bytes.Buffer
	cw, err := encryptingWriter(&buf, "correct-password")
	require.NoError(t, err)
	_, err = cw.Write(plaintext)
	require.NoError(t, err)

	cr, err := decryptingReader(&buf, "wrong-password")
	require.NoError(t, err)
	decrypted, err := io.ReadAll(cr)
	require.NoError(t, err)

	assert.NotEqual(t, plaintext, decrypted)
}

// legacyEncrypt reproduces the pre-versioning backup format (no magic/version
// prefix, AES-OFB) to verify decryptingReader still restores old backups.
func legacyEncrypt(t *testing.T, w io.Writer, password string, plaintext []byte) {
	t.Helper()

	salt := make([]byte, 8)
	_, err := rand.Read(salt)
	require.NoError(t, err)

	encKey := pbkdf2.Key([]byte(password), salt, 4096, 32, sha256.New)
	block, err := aes.NewCipher(encKey)
	require.NoError(t, err)

	iv := make([]byte, aes.BlockSize)
	_, err = rand.Read(iv)
	require.NoError(t, err)

	_, err = w.Write(salt)
	require.NoError(t, err)
	_, err = w.Write(iv)
	require.NoError(t, err)

	stream := cipher.NewOFB(block, iv) //nolint:staticcheck // deliberately reproduces the legacy pre-versioning format
	cw := &cipher.StreamWriter{S: stream, W: w}
	_, err = cw.Write(plaintext)
	require.NoError(t, err)
}

func TestBackupDecrypt_LegacyOFBFormat(t *testing.T) {
	password := "test-password"
	plaintext := []byte("this is a fake zip archive of a legacy lokihub backup")

	var buf bytes.Buffer
	legacyEncrypt(t, &buf, password, plaintext)

	cr, err := decryptingReader(&buf, password)
	require.NoError(t, err)
	decrypted, err := io.ReadAll(cr)
	require.NoError(t, err)

	assert.Equal(t, plaintext, decrypted)
}

func TestBackupDecrypt_UnsupportedFormatVersion(t *testing.T) {
	var buf bytes.Buffer
	_, err := buf.Write(backupMagic)
	require.NoError(t, err)
	_, err = buf.Write([]byte{0xFF})
	require.NoError(t, err)

	_, err = decryptingReader(&buf, "any-password")
	assert.ErrorContains(t, err, "unsupported backup format version")
}

// TestRestoreBackup_ZipSlipRejected is the regression test for the zip-slip
// path-traversal guard in RestoreBackup: a crafted backup whose zip entry
// name climbs out of the workdir's restore directory (e.g. "../../evil.txt")
// must be rejected, and must not write anything outside that directory.
func TestRestoreBackup_ZipSlipRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	workDir := t.TempDir()
	svc.Cfg.GetEnv().Workdir = workDir

	password := "test-password"
	const maliciousEntry = "../../evil.txt"

	var encrypted bytes.Buffer
	cw, err := encryptingWriter(&encrypted, password)
	require.NoError(t, err)

	zw := zip.NewWriter(cw)
	zf, err := zw.Create(maliciousEntry)
	require.NoError(t, err)
	_, err = zf.Write([]byte("pwned"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	theAPI := &api{db: svc.DB, cfg: svc.Cfg}
	err = theAPI.RestoreBackup(password, &encrypted)
	require.Error(t, err)
	assert.ErrorContains(t, err, "escapes restore directory")

	// Nothing should have been written outside workDir's own restore
	// directory (in particular, not at the path the traversal targets:
	// workDir's own parent).
	_, statErr := os.Stat(filepath.Join(filepath.Dir(workDir), "evil.txt"))
	assert.True(t, os.IsNotExist(statErr), "zip-slip entry must not have been written outside the restore directory")
}
