package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNip98ReplayStore_RejectsReplayAcrossInstances is the durable half of
// FLSPD-0016 / R-LSP-NIP98REPLAY: a new store instance (a restarted process or
// a second replica) sharing the same table still rejects a previously seen
// event, which the per-process in-memory cache could not.
func TestNip98ReplayStore_RejectsReplayAcrossInstances(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Unix()
	eventID := newOrderId(t) // a distinct key per test run

	s1 := NewNip98ReplayStore(testPool)
	require.NoError(t, s1.CheckAndRecord(ctx, eventID, now), "first use must be recorded")

	s2 := NewNip98ReplayStore(testPool)
	err := s2.CheckAndRecord(ctx, eventID, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replay")
}

// TestNip98ReplayStore_DistinctEventsAccepted confirms two different events
// are both recorded (no false replay).
func TestNip98ReplayStore_DistinctEventsAccepted(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Unix()
	s := NewNip98ReplayStore(testPool)

	require.NoError(t, s.CheckAndRecord(ctx, newOrderId(t), now))
	require.NoError(t, s.CheckAndRecord(ctx, newOrderId(t), now))
}

// TestNip98ReplayStore_ExpiredEntryPrunedAndReaccepted checks that once an
// event id's retention has elapsed it is pruned and the id is treated as a
// fresh (non-replay) use again -- matching the ±window semantics (by the time
// a row expires the event's timestamp is itself too old to pass).
func TestNip98ReplayStore_ExpiredEntryPrunedAndReaccepted(t *testing.T) {
	ctx := context.Background()
	s := NewNip98ReplayStore(testPool)
	eventID := newOrderId(t)

	// Record with a now far enough in the past that its retention has already
	// lapsed relative to the current time.
	past := time.Now().Unix() - (replayRetentionSeconds + replayPruneIntervalSeconds + 10)
	require.NoError(t, s.CheckAndRecord(ctx, eventID, past))

	// A later call prunes the expired row (interval elapsed), so the same id
	// inserts cleanly rather than colliding as a replay.
	require.NoError(t, s.CheckAndRecord(ctx, eventID, time.Now().Unix()))
}
