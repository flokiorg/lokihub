package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// randomHexPubkey generates a fresh 33-byte hex pubkey so each test gets an
// isolated row instead of colliding with other tests sharing testPool.
func randomHexPubkey(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 33)
	_, err := rand.Read(buf)
	require.NoError(t, err)
	return hex.EncodeToString(buf)
}

func TestNotificationsStore_RegisterAndGetRegistrations(t *testing.T) {
	store := NewNotificationsStore(testPool)
	ctx := context.Background()
	pubkey := randomHexPubkey(t)

	require.NoError(t, store.Register(ctx, pubkey, "https://example.com/hook", "push"))

	regs, err := store.GetRegistrations(ctx, pubkey)
	require.NoError(t, err)
	require.Len(t, regs, 1)
	assert.Equal(t, "https://example.com/hook", regs[0].Url)
	assert.Equal(t, "push", regs[0].Transport)
}

func TestNotificationsStore_RegisterUpsertsTransportOnConflict(t *testing.T) {
	store := NewNotificationsStore(testPool)
	ctx := context.Background()
	pubkey := randomHexPubkey(t)

	require.NoError(t, store.Register(ctx, pubkey, "https://example.com/hook", "push"))
	require.NoError(t, store.Register(ctx, pubkey, "https://example.com/hook", "nostr"))

	regs, err := store.GetRegistrations(ctx, pubkey)
	require.NoError(t, err)
	require.Len(t, regs, 1, "same pubkey+url must upsert in place, not duplicate")
	assert.Equal(t, "nostr", regs[0].Transport)
}

func TestNotificationsStore_Unsubscribe(t *testing.T) {
	store := NewNotificationsStore(testPool)
	ctx := context.Background()
	pubkey := randomHexPubkey(t)

	require.NoError(t, store.Register(ctx, pubkey, "https://example.com/hook", "push"))
	require.NoError(t, store.Unsubscribe(ctx, pubkey, "https://example.com/hook"))

	regs, err := store.GetRegistrations(ctx, pubkey)
	require.NoError(t, err)
	assert.Empty(t, regs)
}

func TestNotificationsStore_RemoveExpired(t *testing.T) {
	store := NewNotificationsStore(testPool)
	ctx := context.Background()
	staleKey := randomHexPubkey(t)
	freshKey := randomHexPubkey(t)

	require.NoError(t, store.Register(ctx, staleKey, "https://example.com/stale", "push"))
	require.NoError(t, store.Register(ctx, freshKey, "https://example.com/fresh", "push"))

	// Force the "stale" row's refreshed_at into the past directly, since
	// Register always stamps "now".
	pk, err := hex.DecodeString(staleKey)
	require.NoError(t, err)
	_, err = testPool.Exec(ctx,
		`UPDATE public.notification_subscriptions SET refreshed_at = $1 WHERE pubkey = $2`,
		time.Now().Add(-48*time.Hour).UnixMicro(), pk)
	require.NoError(t, err)

	require.NoError(t, store.RemoveExpired(ctx, time.Now().Add(-24*time.Hour)))

	staleRegs, err := store.GetRegistrations(ctx, staleKey)
	require.NoError(t, err)
	assert.Empty(t, staleRegs, "registrations refreshed before the cutoff must be removed")

	freshRegs, err := store.GetRegistrations(ctx, freshKey)
	require.NoError(t, err)
	assert.Len(t, freshRegs, 1, "registrations refreshed after the cutoff must survive")
}

func TestNotificationsStore_EnqueueAndGetPending(t *testing.T) {
	store := NewNotificationsStore(testPool)
	ctx := context.Background()
	pubkey := randomHexPubkey(t)

	id, err := store.Enqueue(ctx, pubkey, "https://example.com/hook", "push", "lsps5.webhook.payment_incoming",
		map[string]interface{}{"payment_hash": "abc123"})
	require.NoError(t, err)
	assert.Positive(t, id)

	items, err := store.GetPending(ctx, 10)
	require.NoError(t, err)

	foundIdx := -1
	for i, item := range items {
		if item.ID == id {
			foundIdx = i
			break
		}
	}
	require.NotEqual(t, -1, foundIdx, "the enqueued item must appear in GetPending")

	item := items[foundIdx]
	assert.Equal(t, pubkey, item.Pubkey)
	assert.Equal(t, "https://example.com/hook", item.TargetURL)
	assert.Equal(t, "push", item.Transport)
	assert.Equal(t, "lsps5.webhook.payment_incoming", item.Method)
	assert.Equal(t, 0, item.Attempts)

	params, ok := item.Params.(map[string]interface{})
	require.True(t, ok, "JSONB params must decode back into a map")
	assert.Equal(t, "abc123", params["payment_hash"])
}

func TestNotificationsStore_UpdateStatusExcludesFromPending(t *testing.T) {
	store := NewNotificationsStore(testPool)
	ctx := context.Background()
	pubkey := randomHexPubkey(t)

	id, err := store.Enqueue(ctx, pubkey, "https://example.com/hook", "push", "some.method", map[string]interface{}{})
	require.NoError(t, err)

	require.NoError(t, store.UpdateStatus(ctx, id, "completed", time.Now()))

	items, err := store.GetPending(ctx, 100)
	require.NoError(t, err)
	for _, item := range items {
		assert.NotEqual(t, id, item.ID, "a completed item must not show up as pending")
	}
}

func TestNotificationsStore_UpdateStatusBackoffRequeuesForLater(t *testing.T) {
	store := NewNotificationsStore(testPool)
	ctx := context.Background()
	pubkey := randomHexPubkey(t)

	id, err := store.Enqueue(ctx, pubkey, "https://example.com/hook", "push", "some.method", map[string]interface{}{})
	require.NoError(t, err)

	future := time.Now().Add(time.Hour)
	require.NoError(t, store.UpdateStatus(ctx, id, "failed", future))

	items, err := store.GetPending(ctx, 100)
	require.NoError(t, err)
	for _, item := range items {
		assert.NotEqual(t, id, item.ID, "an item retried an hour from now must not be immediately pending")
	}

	var attempts int
	require.NoError(t, testPool.QueryRow(ctx, `SELECT attempts FROM public.notification_queue WHERE id = $1`, id).Scan(&attempts))
	assert.Equal(t, 1, attempts, "UpdateStatus must increment attempts")
}
