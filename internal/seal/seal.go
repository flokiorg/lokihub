// Package seal is AES-256-GCM sealing of strings under a 32-byte key. It is a
// leaf: it imports no lokihub code, so config (which wraps its secrets with it)
// and atrest (which seals the Hub's own database secrets) can both use it
// without depending on each other.
//
// A sealed value is "<hex nonce>-<hex ciphertext>".
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeyLen is the only key length accepted (AES-256).
const KeyLen = 32

// EncryptWithKey seals plaintext under key.
func EncryptWithKey(plaintext string, key []byte) (string, error) {
	if len(key) != KeyLen {
		return "", fmt.Errorf("key must be %d bytes, got %d", KeyLen, len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(nonce) + "-" + hex.EncodeToString(ciphertext), nil
}

// DecryptWithKey opens a value EncryptWithKey made.
func DecryptWithKey(sealed string, key []byte) (string, error) {
	if len(key) != KeyLen {
		return "", fmt.Errorf("key must be %d bytes, got %d", KeyLen, len(key))
	}

	arr := strings.Split(sealed, "-")
	if len(arr) != 2 {
		return "", errors.New("seal: malformed sealed value")
	}
	nonce, err := hex.DecodeString(arr[0])
	if err != nil {
		return "", fmt.Errorf("seal: invalid nonce: %w", err)
	}
	data, err := hex.DecodeString(arr[1])
	if err != nil {
		return "", fmt.Errorf("seal: invalid ciphertext: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(nonce) != gcm.NonceSize() {
		return "", errors.New("seal: invalid nonce length")
	}

	plaintext, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
