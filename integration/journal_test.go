//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"testing"

	"github.com/ohstr/nmilat/nipcash"
)

// memJournal is an in-memory client.Journal: the suite writes every
// bill-creating call ahead of sending it, as a client must (NIP-CASH §request_id).
type memJournal struct {
	mu      sync.Mutex
	calls   []nipcash.PendingCall
	derived map[string]int64
}

func newMemJournal() *memJournal { return &memJournal{derived: map[string]int64{}} }

func (j *memJournal) WriteAhead(_ context.Context, calls []nipcash.PendingCall) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.calls = append(j.calls, calls...)
	return nil
}

func (j *memJournal) RecordDerived(_ context.Context, id string, keepUntil int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.derived[id] = keepUntil
	return nil
}

func (j *memJournal) DerivedPlaced(_ context.Context, id string) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, ok := j.derived[id]
	return ok, nil
}

// newRequestID is a fresh request_id: 64 lowercase hex characters.
func newRequestID(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// testDevice names this suite as the writing device in a journal.
const testDevice = "lokihub-integration"
