package lnclient

import (
	"encoding/hex"
	"fmt"
)

// DecodeHash32 decodes a hex string that must be exactly 32 bytes — a payment
// hash, a preimage or a description hash. what names the value in the error.
func DecodeHash32(what, s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%s must be 32 bytes hex: %w", what, err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("%s must be 32 bytes hex", what)
	}
	return b, nil
}
