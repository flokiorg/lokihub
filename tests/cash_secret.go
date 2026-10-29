package tests

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// GenerateCashSecret is a fresh cash secret and its sha256 commitment, both hex,
// as a caller of mint_cash or cash_transfer generates them.
func GenerateCashSecret() (secretHex, commitmentHex string, err error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(secret[:])
	return hex.EncodeToString(secret[:]), hex.EncodeToString(sum[:]), nil
}
