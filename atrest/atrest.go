// Package atrest seals the secrets lokihub keeps in its database for its own
// later use, such as the preimage of a hold invoice the Hub settles itself. The
// key is derived from the node's mnemonic when the node unlocks, so the
// database alone opens nothing; the sealing is config's AES-GCM.
package atrest

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"

	"github.com/flokiorg/lokihub/config"
)

// prefix marks a sealed value and its format version.
const prefix = "v1:"

// ErrLocked: no key is loaded, as before the node unlocks.
var ErrLocked = errors.New("atrest: the at-rest key is not loaded")

var (
	mu  sync.RWMutex
	key []byte
)

// DeriveKey is the at-rest key of the app key's private bytes.
func DeriveKey(appKeyPrivate []byte) []byte {
	mac := hmac.New(sha256.New, []byte("lokihub at-rest v1"))
	mac.Write(appKeyPrivate)
	return mac.Sum(nil)
}

// SetKey loads the at-rest key (32 bytes).
func SetKey(k []byte) {
	mu.Lock()
	defer mu.Unlock()
	key = append([]byte(nil), k...)
}

func current() ([]byte, error) {
	mu.RLock()
	defer mu.RUnlock()
	if len(key) != 32 {
		return nil, ErrLocked
	}
	return key, nil
}

// Seal encrypts plaintext under the at-rest key.
func Seal(plaintext string) (string, error) {
	k, err := current()
	if err != nil {
		return "", err
	}
	sealed, err := config.AesGcmEncryptWithKey(plaintext, k)
	if err != nil {
		return "", err
	}
	return prefix + sealed, nil
}

// Open decrypts a value Seal made; a value of another format or key fails.
func Open(sealed string) (string, error) {
	k, err := current()
	if err != nil {
		return "", err
	}
	body, ok := strings.CutPrefix(sealed, prefix)
	if !ok || strings.Count(body, "-") != 1 {
		return "", errors.New("atrest: not a sealed value")
	}
	return config.AesGcmDecryptWithKey(body, k)
}
