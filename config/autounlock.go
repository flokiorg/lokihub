package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokiorg/lokihub/seal"
)

// The auto-unlock password is the KEK that decrypts the mnemonic and every
// Encrypted=true config secret. Storing it in cleartext in the same nwc.db
// nullifies the at-rest encryption for a user who enables auto-unlock (the key
// sits beside the ciphertext it unlocks). Instead it is encrypted at rest under
// a host-local key kept OUTSIDE the database, in a 0600 file in the workdir, so
// a copy of nwc.db alone (a backup, a volume snapshot, a group/other read) no
// longer yields the KEK.
//
// Residual risk: the host key file lives in the same workdir as the DB, so an
// attacker who reads the whole workdir still gets both. This is a deliberate
// "keep the KEK out of the DB file itself" step, gated by the workdir being
// 0700 (LOKIHUB-0036) and auto-unlock staying opt-in with backup/password-change
// refused while it is on. A stronger store (OS keyring / TPM) is left as future
// work.
const (
	// autoUnlockEncPrefix marks an AutoUnlockPassword value encrypted at rest.
	// A stored value without it is a legacy cleartext password, migrated on read.
	autoUnlockEncPrefix = "aesk:"
	// autoUnlockKeyFile is the host-local key, relative to the workdir.
	autoUnlockKeyFile = "autounlock.key"
)

func isEncryptedAutoUnlock(stored string) bool {
	return strings.HasPrefix(stored, autoUnlockEncPrefix)
}

func (cfg *config) autoUnlockKeyPath() string {
	return filepath.Join(cfg.GetDefaultWorkDir(), autoUnlockKeyFile)
}

// autoUnlockHostKey loads the 32-byte host-local key, creating it (0600) on
// first use. It never regenerates an existing-but-unreadable key: that would
// make an already-encrypted auto-unlock password undecryptable.
func (cfg *config) autoUnlockHostKey() ([]byte, error) {
	cfg.autoUnlockKeyMu.Lock()
	defer cfg.autoUnlockKeyMu.Unlock()

	path := cfg.autoUnlockKeyPath()
	data, err := os.ReadFile(path)
	if err == nil {
		key, derr := hex.DecodeString(strings.TrimSpace(string(data)))
		if derr != nil || len(key) != 32 {
			return nil, errors.New("auto-unlock host key file is invalid")
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0600); err != nil {
		return nil, err
	}
	return key, nil
}

func (cfg *config) encryptAutoUnlock(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	key, err := cfg.autoUnlockHostKey()
	if err != nil {
		return "", err
	}
	ciphertext, err := seal.EncryptWithKey(plaintext, key)
	if err != nil {
		return "", err
	}
	return autoUnlockEncPrefix + ciphertext, nil
}

func (cfg *config) decryptAutoUnlock(stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !isEncryptedAutoUnlock(stored) {
		return stored, nil
	}
	key, err := cfg.autoUnlockHostKey()
	if err != nil {
		return "", err
	}
	return seal.DecryptWithKey(strings.TrimPrefix(stored, autoUnlockEncPrefix), key)
}
