package config

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tyler-smith/go-bip32"
	"github.com/tyler-smith/go-bip39"
	"golang.org/x/crypto/argon2"

	"github.com/flokiorg/lokihub/seal"
)

func TestDecryptExistingCiphertextWithPassword(t *testing.T) {
	value, err := AesGcmDecryptWithPassword("323f41394d3175b72454ccae9c0081f94df5fb4c2fb0b9283a87e5aafba81839-c335b9eeea75c28a6f823354-5055b90dadbdd01c52fbdbb7efb80609e4410357481651e89ceb1501c8e1dea1f33a8e3322a1cef4f641773667423bca5154dfeccac390cfcd719b36965adc3e6ae56fd5d6c82819596e9ef4ff07193ae345eb291fa412a1ce6066864b", "123")
	assert.NoError(t, err)
	assert.Equal(t, "connect maximum march lava ignore resist visa kind kiwi kidney develop animal", value)
}

func TestEncryptDecryptWithPassword(t *testing.T) {
	mnemonic := "connect maximum march lava ignore resist visa kind kiwi kidney develop animal"
	encrypted, err := AesGcmEncryptWithPassword(mnemonic, "123")
	assert.NoError(t, err)
	value, err := AesGcmDecryptWithPassword(encrypted, "123")
	assert.NoError(t, err)
	assert.Equal(t, mnemonic, value)
}

// TestEncryptWithPassword_UsesArgon2idParams pins LOKIHUB-0035: new at-rest
// secrets are wrapped with Argon2id at >= 64 MiB memory (RFC 9106 / OWASP), and
// the KDF params travel in a self-describing header.
func TestEncryptWithPassword_UsesArgon2idParams(t *testing.T) {
	encrypted, err := AesGcmEncryptWithPassword("secret", "pw")
	require.NoError(t, err)

	header := strings.SplitN(encrypted, "-", 2)[0]
	parts := strings.Split(header, "$")
	require.Len(t, parts, 5, "header must be argon2id$time$mem$threads$salt")
	assert.Equal(t, "argon2id", parts[0], "KEK must use Argon2id, not Argon2i")

	mem, err := strconv.Atoi(parts[2])
	require.NoError(t, err)
	assert.GreaterOrEqual(t, mem, 64*1024, "Argon2id memory must be >= 64 MiB")

	assert.False(t, isLegacyWrappedSecret(encrypted), "a freshly wrapped secret must not look legacy")

	// Params match the hardened constants.
	assert.Equal(t, strconv.Itoa(kekArgon2idTime), parts[1])
	assert.Equal(t, strconv.Itoa(kekArgon2idThreads), parts[3])
}

// legacyArgon2iWrap reproduces the pre-hardening at-rest format: a bare hex
// salt with the value wrapped under an Argon2i-derived KEK (t=3, m=32 MiB, p=1).
func legacyArgon2iWrap(t *testing.T, plaintext, password string) string {
	t.Helper()
	salt := make([]byte, kekSaltLen)
	_, err := rand.Read(salt)
	require.NoError(t, err)
	key := argon2.Key([]byte(password), salt, kekLegacyArgon2iTime, kekLegacyArgon2iMemory, kekLegacyArgon2iThreads, 32)
	ciphertext, err := seal.EncryptWithKey(plaintext, key)
	require.NoError(t, err)
	return hex.EncodeToString(salt) + "-" + ciphertext
}

// TestDecrypt_LegacyArgon2iBlobStillUnwraps pins the back-compat half of
// LOKIHUB-0035: a secret wrapped before the hardening (bare hex salt, Argon2i
// KEK) must still decrypt, and must be recognised as a re-wrap candidate.
func TestDecrypt_LegacyArgon2iBlobStillUnwraps(t *testing.T) {
	plaintext := "a secret sealed by the pre-hardening Argon2i KEK"
	password := "unlock-pw"

	legacy := legacyArgon2iWrap(t, plaintext, password)
	assert.True(t, isLegacyWrappedSecret(legacy), "legacy blob must be detected for migration")

	value, err := AesGcmDecryptWithPassword(legacy, password)
	require.NoError(t, err)
	assert.Equal(t, plaintext, value)
}

func TestDecryptExistingCiphertextWithKey(t *testing.T) {
	mnemonic := "connect maximum march lava ignore resist visa kind kiwi kidney develop animal"
	masterKey, err := bip32.NewMasterKey(bip39.NewSeed(mnemonic, ""))
	assert.NoError(t, err)
	value, err := seal.DecryptWithKey("22ad485dea4f49696594c7c4-afe35ce65fc5a45249bf1b9078472fb28395fc88c30a79c76c7d8d37cf", masterKey.Key)
	assert.NoError(t, err)
	assert.Equal(t, "Hello, world!", value)
}

func TestEncryptDecryptWithKey(t *testing.T) {
	plaintext := "Hello, world!"
	mnemonic := "connect maximum march lava ignore resist visa kind kiwi kidney develop animal"
	masterKey, err := bip32.NewMasterKey(bip39.NewSeed(mnemonic, ""))
	assert.NoError(t, err)

	assert.Equal(t, "409e902eafba273b21dff921f0eb4bec6cbb0b657fdce8d245ca78d2920f8b73", hex.EncodeToString(masterKey.Key))

	encrypted, err := seal.EncryptWithKey(plaintext, masterKey.Key)
	assert.NoError(t, err)
	value, err := seal.DecryptWithKey(encrypted, masterKey.Key)
	assert.NoError(t, err)
	assert.Equal(t, plaintext, value)
}
