package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func randomNodeID(t *testing.T) []byte {
	t.Helper()
	id := make([]byte, 33)
	_, err := rand.Read(id)
	require.NoError(t, err)
	id[0] = 0x02
	return id
}

func TestAnnounceStateStore_DefaultOpen(t *testing.T) {
	store := NewAnnounceStateStore(testPool)
	st, err := store.GetAnnounceState(context.Background(), randomNodeID(t))
	require.NoError(t, err)
	assert.Equal(t, core.AnnounceOpen, st.Status)
	assert.True(t, st.UpdatedAt.IsZero())
}

func TestAnnounceStateStore_Transitions(t *testing.T) {
	ctx := context.Background()
	store := NewAnnounceStateStore(testPool)
	node := randomNodeID(t)

	// resume on a never-changed node stays open and is not a change.
	st, err := store.ApplyAnnounceAction(ctx, node, core.AnnounceResume)
	require.NoError(t, err)
	assert.Equal(t, core.AnnounceOpen, st.Status)
	assert.True(t, st.UpdatedAt.IsZero())

	st, err = store.ApplyAnnounceAction(ctx, node, core.AnnouncePause)
	require.NoError(t, err)
	assert.Equal(t, core.AnnouncePaused, st.Status)
	assert.False(t, st.UpdatedAt.IsZero())

	got, err := store.GetAnnounceState(ctx, node)
	require.NoError(t, err)
	assert.Equal(t, core.AnnouncePaused, got.Status)
	assert.Equal(t, st.UpdatedAt.UnixMicro(), got.UpdatedAt.UnixMicro())

	st, err = store.ApplyAnnounceAction(ctx, node, core.AnnounceUnlist)
	require.NoError(t, err)
	assert.Equal(t, core.AnnounceClosed, st.Status)

	_, err = store.ApplyAnnounceAction(ctx, node, core.AnnouncePause)
	assert.True(t, errors.Is(err, core.ErrAnnounceTransition))
	got, _ = store.GetAnnounceState(ctx, node)
	assert.Equal(t, core.AnnounceClosed, got.Status, "a refused action changes nothing")

	st, err = store.ApplyAnnounceAction(ctx, node, core.AnnounceResume)
	require.NoError(t, err)
	assert.Equal(t, core.AnnounceOpen, st.Status)

	// Nodes are independent.
	other, err := store.GetAnnounceState(ctx, randomNodeID(t))
	require.NoError(t, err)
	assert.Equal(t, core.AnnounceOpen, other.Status)
}

func TestAnnounceStateStore_RejectsBadStatusInDB(t *testing.T) {
	_, err := testPool.Exec(context.Background(),
		`INSERT INTO public.announce_state (node_id, status, updated_at) VALUES ($1, 'bogus', 1)`, randomNodeID(t))
	assert.Error(t, err, "the status CHECK constraint must refuse other values")
}

// Concurrent actions are serialized by the row lock: after N pauses racing
// one unlist, the result is closed whenever unlist ran last, and every
// pause after the unlist is refused rather than reopening anything.
func TestAnnounceStateStore_ConcurrentActions(t *testing.T) {
	ctx := context.Background()
	store := NewAnnounceStateStore(testPool)
	node := randomNodeID(t)

	var wg sync.WaitGroup
	errs := make(chan error, 21)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.ApplyAnnounceAction(ctx, node, core.AnnouncePause)
			errs <- err
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := store.ApplyAnnounceAction(ctx, node, core.AnnounceUnlist)
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			require.ErrorIs(t, err, core.ErrAnnounceTransition)
		}
	}
	st, err := store.GetAnnounceState(ctx, node)
	require.NoError(t, err)
	assert.Equal(t, core.AnnounceClosed, st.Status)
}

func TestAnnounceStateStore_ClosedPublished(t *testing.T) {
	ctx := context.Background()
	store := NewAnnounceStateStore(testPool)
	node := randomNodeID(t)
	at := time.UnixMicro(1792713600_000000)

	ok, err := store.MarkClosedPublished(ctx, node, at)
	require.NoError(t, err)
	assert.False(t, ok, "an open node (no row) is not marked")

	_, err = store.ApplyAnnounceAction(ctx, node, core.AnnounceUnlist)
	require.NoError(t, err)
	ok, err = store.MarkClosedPublished(ctx, node, at)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = store.MarkClosedPublished(ctx, node, at.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, ok, "marked once")

	st, err := store.GetAnnounceState(ctx, node)
	require.NoError(t, err)
	assert.Equal(t, at.UnixMicro(), st.ClosedPublishedAt.UnixMicro())

	// A no-op unlist keeps the mark; a resume clears it.
	st, err = store.ApplyAnnounceAction(ctx, node, core.AnnounceUnlist)
	require.NoError(t, err)
	assert.False(t, st.ClosedPublishedAt.IsZero())
	st, err = store.ApplyAnnounceAction(ctx, node, core.AnnounceResume)
	require.NoError(t, err)
	assert.True(t, st.ClosedPublishedAt.IsZero())
	st, _ = store.GetAnnounceState(ctx, node)
	assert.True(t, st.ClosedPublishedAt.IsZero())

	// Unlisting again needs a new publish.
	_, err = store.ApplyAnnounceAction(ctx, node, core.AnnounceUnlist)
	require.NoError(t, err)
	ok, err = store.MarkClosedPublished(ctx, node, at)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestAnnounceStateStore_PublisherColumns(t *testing.T) {
	ctx := context.Background()
	store := NewAnnounceStateStore(testPool)
	node := randomNodeID(t)
	at := time.UnixMicro(1792713600_000000)

	// Each setter works on a node with no row yet, and leaves it open.
	require.NoError(t, store.SetSelfCheckBlock(ctx, node, "self-check failed: x"))
	st, err := store.GetAnnounceState(ctx, node)
	require.NoError(t, err)
	assert.Equal(t, core.AnnounceOpen, st.Status)
	assert.Equal(t, "self-check failed: x", st.SelfCheckBlock)
	assert.True(t, st.LastPublishedAt.IsZero())

	assert.Zero(t, st.LastCreatedAt)
	assert.Nil(t, st.LastFields)

	require.NoError(t, store.SetLastCreatedAt(ctx, node, 1792713601))
	require.NoError(t, store.MarkPublished(ctx, node, at, []byte(`{"Status":"open"}`)))
	require.NoError(t, store.MarkOffClosedPublished(ctx, node, at.Add(time.Second)))
	st, _ = store.GetAnnounceState(ctx, node)
	assert.Equal(t, at.UnixMicro(), st.LastPublishedAt.UnixMicro())
	assert.Equal(t, int64(1792713601), st.LastCreatedAt)
	assert.JSONEq(t, `{"Status":"open"}`, string(st.LastFields))
	assert.Equal(t, at.Add(time.Second).UnixMicro(), st.OffClosedPublishedAt.UnixMicro())
	assert.Equal(t, "self-check failed: x", st.SelfCheckBlock, "other columns kept")

	// A status change does not touch them.
	_, err = store.ApplyAnnounceAction(ctx, node, core.AnnouncePause)
	require.NoError(t, err)
	st, _ = store.GetAnnounceState(ctx, node)
	assert.Equal(t, core.AnnouncePaused, st.Status)
	assert.False(t, st.OffClosedPublishedAt.IsZero())
	assert.NotEmpty(t, st.SelfCheckBlock)

	require.NoError(t, store.ClearOffClosed(ctx, node))
	require.NoError(t, store.SetSelfCheckBlock(ctx, node, ""))
	st, _ = store.GetAnnounceState(ctx, node)
	assert.True(t, st.OffClosedPublishedAt.IsZero())
	assert.Empty(t, st.SelfCheckBlock)
	assert.Equal(t, core.AnnouncePaused, st.Status)
}
