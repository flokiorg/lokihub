package api

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/pbkdf2"
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
