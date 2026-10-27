package controllers

import (
	"crypto/rand"
	"encoding/hex"
)

// testRequestID is a fresh request_id (NIP-CASH §request_id), 64 lowercase hex.
func testRequestID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
