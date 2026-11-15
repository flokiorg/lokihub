package controllers

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/flokiorg/lokihub/nip47/models"
)

// testRequestID is a fresh request_id (NIP-CASH §request_id), 64 lowercase hex.
func testRequestID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// testMintMeta is a standard-transport call that arrived now with a deadline a
// minute ahead, inside any horizon.
func testMintMeta() models.CallMeta {
	now := time.Now().Unix()
	return models.CallMeta{Transport: models.TransportStandard, ArrivedAt: now, Deadline: now + 60, DeadlinePresent: true}
}
