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
	// Close seals the final authenticated chunk; the current format is an AEAD
	// stream, so the archive is incomplete until it is closed.
	require.NoError(t, cw.Close())

	cr, err := decryptingReader(&buf, password)
	require.NoError(t, err)
	decrypted, err := io.ReadAll(cr)
	require.NoError(t, err)

	assert.Equal(t, plaintext, decrypted)
}

// TestBackupEncryptDecrypt_RoundTripLarge exercises the chunked AEAD framing
// across many full chunks plus a partial tail, so the chunk-boundary and
// final-chunk handling is covered rather than just a single sub-chunk payload.
func TestBackupEncryptDecrypt_RoundTripLarge(t *testing.T) {
	password := "test-password"
	plaintext := make([]byte, backupGCMChunkSize*3+123)
	_, err := rand.Read(plaintext)
	require.NoError(t, err)

	var buf bytes.Buffer
	cw, err := encryptingWriter(&buf, password)
	require.NoError(t, err)
	_, err = cw.Write(plaintext)
	require.NoError(t, err)
	require.NoError(t, cw.Close())

	cr, err := decryptingReader(&buf, password)
	require.NoError(t, err)
	decrypted, err := io.ReadAll(cr)
	require.NoError(t, err)

	assert.Equal(t, plaintext, decrypted)
}

// TestBackupEncryptDecrypt_WrongPassword pins LOKIHUB-0034: a wrong password
// must now FAIL cleanly (AEAD authentication error) rather than yielding silent
// garbage the way the old unauthenticated AES-CTR format did.
func TestBackupEncryptDecrypt_WrongPassword(t *testing.T) {
	plaintext := []byte("this is a fake zip archive of a lokihub backup")

	var buf bytes.Buffer
	cw, err := encryptingWriter(&buf, "correct-password")
	require.NoError(t, err)
	_, err = cw.Write(plaintext)
	require.NoError(t, err)
	require.NoError(t, cw.Close())

	cr, err := decryptingReader(&buf, "wrong-password")
	require.NoError(t, err)
	_, err = io.ReadAll(cr)
	require.Error(t, err)
	assert.ErrorContains(t, err, "authentication failed")
}

// TestBackupDecrypt_TamperedFails pins LOKIHUB-0034: flipping a byte of the
// ciphertext must be detected as an authentication failure, not silently
// restored (the malleability the old AES-CTR format allowed).
func TestBackupDecrypt_TamperedFails(t *testing.T) {
	password := "test-password"
	plaintext := []byte("this is a fake zip archive of a lokihub backup")

	var buf bytes.Buffer
	cw, err := encryptingWriter(&buf, password)
	require.NoError(t, err)
	_, err = cw.Write(plaintext)
	require.NoError(t, err)
	require.NoError(t, cw.Close())

	// Flip a bit in the ciphertext body (past the magic/version/salt header).
	b := buf.Bytes()
	tamperAt := len(backupMagic) + 1 + backupSaltLenCurrent + 2
	require.Less(t, tamperAt, len(b))
	b[tamperAt] ^= 0x01

	cr, err := decryptingReader(bytes.NewReader(b), password)
	require.NoError(t, err)
	_, err = io.ReadAll(cr)
	require.Error(t, err)
	assert.ErrorContains(t, err, "authentication failed")
}

// TestBackupDecrypt_TruncatedFails pins LOKIHUB-0034: dropping the final
// authenticated chunk must be rejected as truncation, not accepted as a
// complete (shorter) archive.
func TestBackupDecrypt_TruncatedFails(t *testing.T) {
	password := "test-password"
	plaintext := make([]byte, backupGCMChunkSize*2+10)
	_, err := rand.Read(plaintext)
	require.NoError(t, err)

	var buf bytes.Buffer
	cw, err := encryptingWriter(&buf, password)
	require.NoError(t, err)
	_, err = cw.Write(plaintext)
	require.NoError(t, err)
	require.NoError(t, cw.Close())

	// Drop the trailing (final, short) chunk so the stream ends after a
	// non-final chunk.
	b := buf.Bytes()
	truncated := b[:len(backupMagic)+1+backupSaltLenCurrent+backupGCMEncChunkSize]

	cr, err := decryptingReader(bytes.NewReader(truncated), password)
	require.NoError(t, err)
	_, err = io.ReadAll(cr)
	require.Error(t, err)
	assert.ErrorContains(t, err, "truncated")
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

// TestBackupEncrypt_UsesAEADFormat pins LOKIHUB-0034 (and LOKIHUB-0032): new
// backups use the authenticated argon2id AES-256-GCM format — the GCM version
// marker and a >=16-byte argon2id salt in the header (no IV: the AEAD format
// uses per-chunk counter nonces, not a stored IV).
func TestBackupEncrypt_UsesAEADFormat(t *testing.T) {
	var buf bytes.Buffer
	cw, err := encryptingWriter(&buf, "test-password")
	require.NoError(t, err)
	_, err = cw.Write([]byte("payload"))
	require.NoError(t, err)
	require.NoError(t, cw.Close())

	b := buf.Bytes()
	require.GreaterOrEqual(t, len(b), len(backupMagic)+1+backupSaltLenCurrent)
	require.Equal(t, backupMagic, b[:len(backupMagic)])
	require.Equal(t, byte(backupFormatArgon2idGCM), b[len(backupMagic)], "new backups must use the authenticated AES-GCM format")
	require.GreaterOrEqual(t, backupSaltLenCurrent, 16, "salt must be at least 16 bytes")
}

// legacyEncryptCTRv2 reproduces the superseded PBKDF2-4096 AES-CTR format
// (magic + version 0x02 + 8-byte salt), to verify it still restores.
func legacyEncryptCTRv2(t *testing.T, w io.Writer, password string, plaintext []byte) {
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

	_, err = w.Write(backupMagic)
	require.NoError(t, err)
	_, err = w.Write([]byte{backupFormatCTR})
	require.NoError(t, err)
	_, err = w.Write(salt)
	require.NoError(t, err)
	_, err = w.Write(iv)
	require.NoError(t, err)

	cw := &cipher.StreamWriter{S: cipher.NewCTR(block, iv), W: w}
	_, err = cw.Write(plaintext)
	require.NoError(t, err)
}

func TestBackupDecrypt_LegacyPBKDF2CTRv2(t *testing.T) {
	password := "test-password"
	plaintext := []byte("a backup written by the superseded PBKDF2-4096 format")

	var buf bytes.Buffer
	legacyEncryptCTRv2(t, &buf, password, plaintext)

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
	if svc.DB.Name() != "sqlite" {
		// Restore is refused outright on other backends, before any archive is
		// opened, so the guard under test is never reached there.
		t.Skip("backup restore is supported on sqlite only")
	}

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
	// Seal the final AEAD chunk so the archive decrypts; otherwise restore would
	// fail at the (correct) truncation check before reaching the zip-slip guard.
	require.NoError(t, cw.Close())

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
