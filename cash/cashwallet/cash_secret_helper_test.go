package cashwallet

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// generateCashSecret is a fresh cash secret and its commitment, as a caller makes them.
func generateCashSecret() (secretHex, commitmentHex string, err error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(secret[:])
	return hex.EncodeToString(secret[:]), hex.EncodeToString(sum[:]), nil
}
