package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests"
)

// TestServedNonces_GrowsUnderFloodAndSweepsBackDown covers the nonce-set
// growth + sweep-correctness axis under a flood (the residual D-O3 / G3 gap:
// correctness tests existed, a growth-under-load one did not). Distinct nonces
// accumulate while inside their validity window and the set sweeps back down
// when they lapse, without dropping a still-live nonce or forgetting that a
// live nonce was already served. It is hardware-independent -- it drives
// prune(now) with explicit times rather than measuring wall-clock throughput --
// so it is safe to run in CI with no gate.
func TestServedNonces_GrowsUnderFloodAndSweepsBackDown(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	store := dbServedNonces{db: svc.DB}

	now := time.Now()
	live := now.Add(time.Hour).Unix()      // not_after in the future
	lapsed := now.Add(-time.Minute).Unix() // not_after already past

	const liveCount = 200
	const lapsedCount = 200
	nonce := func(i int) string { return fmt.Sprintf("%064x", i) } // 64 hex chars, distinct per i

	// Growth: flood the set with distinct live + already-lapsed nonces.
	for i := 0; i < liveCount; i++ {
		require.True(t, store.MarkServed(nonce(i), live))
	}
	for i := liveCount; i < liveCount+lapsedCount; i++ {
		require.True(t, store.MarkServed(nonce(i), lapsed))
	}
	assert.EqualValues(t, liveCount+lapsedCount, store.Len(),
		"every distinct nonce is remembered while within its window")

	// Replay protection holds under load: re-serving a known nonce is refused.
	assert.False(t, store.MarkServed(nonce(0), live),
		"a second delivery of the same nonce must be refused")
	assert.True(t, store.Seen(nonce(0)))

	// Sweep: pruning at now drops exactly the lapsed nonces; every live one survives.
	pruned, err := store.prune(now)
	require.NoError(t, err)
	assert.EqualValues(t, lapsedCount, pruned)
	assert.EqualValues(t, liveCount, store.Len(),
		"the set sweeps back down to only the still-live nonces")
	assert.True(t, store.Seen(nonce(0)), "a still-live nonce is not forgotten by the sweep")
	assert.False(t, store.Seen(nonce(liveCount)), "a lapsed nonce is forgotten")

	// Full sweep once the live window passes: the set empties, retaining nothing.
	pruned, err = store.prune(now.Add(2 * time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, liveCount, pruned)
	assert.EqualValues(t, 0, store.Len(), "nothing is retained past its window")
}
