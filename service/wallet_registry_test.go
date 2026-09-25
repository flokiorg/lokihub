package service

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWalletRegistry_AddHasRemove(t *testing.T) {
	r := newWalletRegistry()

	assert.False(t, r.Has("aa"), "empty registry matches nothing")
	assert.Equal(t, 0, r.Len())

	r.Add("aa", "bb")
	assert.True(t, r.Has("aa"))
	assert.True(t, r.Has("bb"))
	assert.False(t, r.Has("cc"), "an unregistered wallet must not match")
	assert.Equal(t, 2, r.Len())

	r.Remove("aa")
	assert.False(t, r.Has("aa"), "a deleted app's wallet must stop matching")
	assert.True(t, r.Has("bb"), "removing one wallet must not disturb the others")
	assert.Equal(t, 1, r.Len())
}

// TestWalletRegistry_AddIsIdempotent matters because a resubscribe re-adds
// every wallet: that must not grow the set or churn the map.
func TestWalletRegistry_AddIsIdempotent(t *testing.T) {
	r := newWalletRegistry()
	r.Add("aa")
	before := r.pubkeys.Load()

	r.Add("aa")

	assert.Equal(t, 1, r.Len())
	assert.Same(t, before, r.pubkeys.Load(), "re-adding a known wallet must not copy the map")
}

func TestWalletRegistry_RemoveUnknownIsNoop(t *testing.T) {
	r := newWalletRegistry()
	r.Add("aa")
	before := r.pubkeys.Load()

	r.Remove("zz")

	assert.Equal(t, 1, r.Len())
	assert.Same(t, before, r.pubkeys.Load(), "removing an unknown wallet must not copy the map")
}

// TestWalletRegistry_ConcurrentAddsAllSurvive guards the CompareAndSwap: two
// apps created at once must both end up registered. A plain Store would drop
// one, and that wallet's requests would then be silently ignored.
func TestWalletRegistry_ConcurrentAddsAllSurvive(t *testing.T) {
	r := newWalletRegistry()

	const writers = 32
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			r.Add("wallet-" + strconv.Itoa(i))
		}(i)
	}
	wg.Wait()

	assert.Equal(t, writers, r.Len())
	for i := 0; i < writers; i++ {
		assert.True(t, r.Has("wallet-"+strconv.Itoa(i)), "wallet %d was lost by a racing write", i)
	}
}

// TestWalletRegistry_ReadsDuringWrites is the -race check for the hot path:
// events arrive while apps are being created and deleted.
func TestWalletRegistry_ReadsDuringWrites(t *testing.T) {
	r := newWalletRegistry()
	r.Add("stable")

	// The writer runs until the readers are done, so the readers get their
	// own WaitGroup: waiting on a single one would block on a writer that
	// only stops once that same wait returns.
	done := make(chan struct{})
	var writer, readers sync.WaitGroup

	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
				key := "churn-" + strconv.Itoa(i%8)
				r.Add(key)
				r.Remove(key)
			}
		}
	}()

	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 5000; j++ {
				if !r.Has("stable") {
					t.Error("a wallet vanished mid-write")
					return
				}
				_ = r.Has("never-registered")
			}
		}()
	}

	readers.Wait()
	close(done)
	writer.Wait()

	assert.True(t, r.Has("stable"))
}

// TestWalletRegistry_RemoveAfterGraceKeepsServingBriefly pins the behaviour a
// deleted app depends on: a request racing the deletion must still be served,
// so HandleEvent can answer it with a proper NIP-47 error instead of the
// caller hanging until its deadline. Removing the wallet the instant its app
// went broke every cash split/spin-off flow in the integration suite.
func TestWalletRegistry_RemoveAfterGraceKeepsServingBriefly(t *testing.T) {
	r := newWalletRegistry()
	r.Add("aa")

	r.RemoveAfterGrace("aa")

	assert.True(t, r.Has("aa"), "the wallet must still be served during the grace period")
	assert.Positive(t, walletRetentionAfterDelete, "the grace period must not be zero")
}

// TestWalletRegistry_RemoveBatchCopiesOnce is the property the variadic
// signature exists for: one copy for the whole batch, not one per pubkey. A
// sweep dropping expired bills from a large registry is where the difference
// stops being academic.
func TestWalletRegistry_RemoveBatchCopiesOnce(t *testing.T) {
	r := newWalletRegistry()
	r.Add("aa", "bb", "cc", "dd")
	before := r.pubkeys.Load()

	r.Remove("aa", "cc")

	assert.False(t, r.Has("aa"))
	assert.False(t, r.Has("cc"))
	assert.True(t, r.Has("bb"), "an untouched wallet must survive the batch")
	assert.True(t, r.Has("dd"))
	assert.Equal(t, 2, r.Len())

	after := r.pubkeys.Load()
	assert.NotSame(t, before, after, "a batch that removes something must swap the map")
}

// TestWalletRegistry_RemoveBatchAllUnknownIsNoop keeps the allocation-free
// path honest: a sweep that finds nothing expired must not copy the map at
// all, which is the common outcome on every tick.
func TestWalletRegistry_RemoveBatchAllUnknownIsNoop(t *testing.T) {
	r := newWalletRegistry()
	r.Add("aa", "bb")
	before := r.pubkeys.Load()

	r.Remove("yy", "zz")

	assert.Equal(t, 2, r.Len())
	assert.Same(t, before, r.pubkeys.Load(), "removing only unknown wallets must not copy the map")
}

// TestWalletRegistry_RemoveEmptyIsNoop guards the degenerate call a batched
// caller makes when its batch is empty.
func TestWalletRegistry_RemoveEmptyIsNoop(t *testing.T) {
	r := newWalletRegistry()
	r.Add("aa")
	before := r.pubkeys.Load()

	r.Remove()

	assert.Equal(t, 1, r.Len())
	assert.Same(t, before, r.pubkeys.Load(), "an empty batch must not copy the map")
}

// TestWalletRegistry_RemoveBatchPartiallyKnown covers the mixed batch a real
// sweep produces, where only some candidates are still registered.
func TestWalletRegistry_RemoveBatchPartiallyKnown(t *testing.T) {
	r := newWalletRegistry()
	r.Add("aa", "bb")

	r.Remove("aa", "zz")

	assert.False(t, r.Has("aa"))
	assert.True(t, r.Has("bb"))
	assert.Equal(t, 1, r.Len())
}
