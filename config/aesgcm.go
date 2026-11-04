package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/flokiorg/lokihub/seal"
)

// At-rest KEK (key-encryption key) parameters. This KEK wraps the Mnemonic and
// every Encrypted=true config secret, so it uses Argon2id (RFC 9106 §4, the
// recommended default over the memory-tradeoff-attackable Argon2i) at the RFC
// 9106 / OWASP second-option memory of 64 MiB.
const (
	kekArgon2idTime    = 3
	kekArgon2idMemory  = 64 * 1024 // KiB (64 MiB)
	kekArgon2idThreads = 4
	kekSaltLen         = 32

	// kekArgon2idTag is the self-describing header prefix on Argon2id-wrapped
	// blobs: "argon2id$<time>$<memKiB>$<threads>$<hexsalt>". It travels with the
	// blob so the params decode without a global version and future tuning stays
	// backward-decryptable. A bare legacy salt is lowercase hex and can never
	// start with this tag (it contains non-hex characters), so detection is
	// unambiguous.
	kekArgon2idTag = "argon2id"
)

// Legacy at-rest KEK parameters: Argon2i at t=3, m=32 MiB, p=1, as written
// before the Argon2id hardening. Read-only — used to unwrap pre-hardening blobs
// so they can be transparently re-wrapped with the current params.
const (
	kekLegacyArgon2iTime    = 3
	kekLegacyArgon2iMemory  = 32 * 1024 // KiB (32 MiB)
	kekLegacyArgon2iThreads = 1
)

// DeriveKey derives the at-rest KEK with Argon2id using the current hardened
// parameters. A nil salt generates a fresh random salt.
func DeriveKey(password string, salt []byte) ([]byte, []byte, error) {
	if salt == nil {
		salt = make([]byte, kekSaltLen)
		if _, err := rand.Read(salt); err != nil {
			return nil, nil, err
		}
	}

	key := argon2.IDKey([]byte(password), salt, kekArgon2idTime, kekArgon2idMemory, kekArgon2idThreads, 32)

	return key, salt, nil
}

func AesGcmEncryptWithPassword(plaintext string, password string) (string, error) {
	secretKey, salt, err := DeriveKey(password, nil)
	if err != nil {
		return "", err
	}

	ciphertext, err := seal.EncryptWithKey(plaintext, secretKey)
	if err != nil {
		return "", err
	}

	// Self-describing Argon2id header (no '-' inside it, so the downstream
	// split-on-'-' decode still yields [header, nonce, data]).
	header := fmt.Sprintf("%s$%d$%d$%d$%s", kekArgon2idTag, kekArgon2idTime, kekArgon2idMemory, kekArgon2idThreads, hex.EncodeToString(salt))

	return header + "-" + ciphertext, nil
}

func AesGcmDecryptWithPassword(ciphertext string, password string) (string, error) {
	arr := strings.Split(ciphertext, "-")
	if len(arr) < 3 {
		return "", fmt.Errorf("invalid encrypted value format")
	}

	secretKey, err := deriveKEKForBlob(arr[0], password)
	if err != nil {
		return "", err
	}

	return seal.DecryptWithKey(arr[1]+"-"+arr[2], secretKey)
}

// deriveKEKForBlob derives the KEK for an encrypted blob from its salt field.
// A tagged "argon2id$..." field is decoded with its embedded params (Argon2id);
// a bare hex salt is a pre-hardening blob, unwrapped with the legacy Argon2i
// params. The embedded params let old blobs keep decrypting after the current
// params are tuned.
func deriveKEKForBlob(saltField string, password string) ([]byte, error) {
	if strings.HasPrefix(saltField, kekArgon2idTag+"$") {
		parts := strings.Split(saltField, "$")
		if len(parts) != 5 {
			return nil, fmt.Errorf("invalid argon2id key-derivation header")
		}
		t, err := strconv.ParseUint(parts[1], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid argon2id time parameter: %w", err)
		}
		m, err := strconv.ParseUint(parts[2], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid argon2id memory parameter: %w", err)
		}
		p, err := strconv.ParseUint(parts[3], 10, 8)
		if err != nil {
			return nil, fmt.Errorf("invalid argon2id threads parameter: %w", err)
		}
		salt, err := hex.DecodeString(parts[4])
		if err != nil {
			return nil, fmt.Errorf("invalid argon2id salt: %w", err)
		}
		return argon2.IDKey([]byte(password), salt, uint32(t), uint32(m), uint8(p), 32), nil
	}

	// Legacy pre-hardening blob: a bare hex salt wrapped with Argon2i.
	salt, err := hex.DecodeString(saltField)
	if err != nil {
		return nil, fmt.Errorf("invalid legacy salt: %w", err)
	}
	return argon2.Key([]byte(password), salt, kekLegacyArgon2iTime, kekLegacyArgon2iMemory, kekLegacyArgon2iThreads, 32), nil
}

// isLegacyWrappedSecret reports whether an at-rest encrypted config value was
// wrapped with the legacy Argon2i KEK (i.e. lacks the Argon2id header), and so
// is a candidate for re-wrapping on unlock.
func isLegacyWrappedSecret(storedValue string) bool {
	return storedValue != "" && !strings.HasPrefix(storedValue, kekArgon2idTag+"$")
}
