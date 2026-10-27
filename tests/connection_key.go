package tests

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"

	"github.com/ohstr/nmilat/nipIC"
)

// TestPlatform is the platform test connection keys and attestations name.
const TestPlatform = nipIC.WebIdentity("discord")

var (
	connKeyMu      sync.Mutex
	connKeyUserIDs = map[string]string{}
)

// NewTestConnectionKey is a fresh connection key derived as NIP-IC requires
// (hex(sha256(platform ":" user_id)) on TestPlatform), remembering its user id so
// an attestation for it can be built.
func NewTestConnectionKey() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000_000))
	if err != nil {
		panic(err)
	}
	userID := fmt.Sprintf("1%017d", n.Int64())
	key, err := nipIC.NewConnectionKey(TestPlatform, userID)
	if err != nil {
		panic(err)
	}
	connKeyMu.Lock()
	connKeyUserIDs[string(key)] = userID
	connKeyMu.Unlock()
	return string(key)
}

// TestConnectionKeyUserID is the user id behind a key from NewTestConnectionKey.
func TestConnectionKeyUserID(key string) (string, bool) {
	connKeyMu.Lock()
	defer connKeyMu.Unlock()
	id, ok := connKeyUserIDs[key]
	return id, ok
}

// TestAttestationEvidence is the evidence tag of a test attestation for key: an
// IA-specific auth_type, so none of public_post's fields are needed.
func TestAttestationEvidence(key string) string {
	userID, ok := TestConnectionKeyUserID(key)
	if !ok {
		userID = "unregistered"
	}
	return fmt.Sprintf(`{"version":1,"platform":%q,"auth_type":"oauth","user_id":%q,"username":"tester","verified_at":1700000000}`, string(TestPlatform), userID)
}
