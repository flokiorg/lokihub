package ttlcache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// A result computed before an invalidation is not stored after it; one computed
// after is, and is answered until the next invalidation.
func TestCache_ResultComputedBeforeAnInvalidationIsNotStored(t *testing.T) {
	c := New[string](time.Minute)
	stale := c.Begin()
	c.Invalidate("conn")
	assert.False(t, c.Put("conn", stale, "open"), "computed before the retirement committed")
	_, ok := c.Get("conn")
	assert.False(t, ok)

	fresh := c.Begin()
	assert.True(t, c.Put("conn", fresh, "retired"))
	got, ok := c.Get("conn")
	assert.True(t, ok)
	assert.Equal(t, "retired", got)

	// An invalidation of another key also drops a computation in flight: it may
	// be for any key.
	inFlight := c.Begin()
	c.Invalidate("other")
	assert.False(t, c.Put("conn", inFlight, "x"))
	got, _ = c.Get("conn")
	assert.Equal(t, "retired", got, "the stored result of conn stays")

	c.Invalidate("conn")
	_, ok = c.Get("conn")
	assert.False(t, ok)
}

// A result older than the cache's ttl is not answered.
func TestCache_ResultExpiresAfterTTL(t *testing.T) {
	c := New[string](20 * time.Millisecond)
	assert.True(t, c.Put("k", c.Begin(), "v"))
	_, ok := c.Get("k")
	assert.True(t, ok)
	time.Sleep(40 * time.Millisecond)
	_, ok = c.Get("k")
	assert.False(t, ok)
}

// An expired entry is dropped when it is found, not kept until invalidated.
func TestCache_ExpiredEntryIsEvictedOnGet(t *testing.T) {
	c := New[string](10 * time.Millisecond)
	assert.True(t, c.Put("k", c.Begin(), "v"))
	time.Sleep(25 * time.Millisecond)
	_, ok := c.Get("k")
	assert.False(t, ok)
	assert.Empty(t, c.entries)
}

// A nil cache holds nothing and needs no nil checks at its call sites.
func TestCache_NilCacheHoldsNothing(t *testing.T) {
	var c *Cache[string]
	assert.False(t, c.Put("k", c.Begin(), "v"))
	_, ok := c.Get("k")
	assert.False(t, ok)
	assert.NotPanics(t, func() { c.Invalidate("k") })
}

func TestCache_KeysAreIndependentAndCachesSeparate(t *testing.T) {
	a, b := New[uint](time.Minute), New[uint](time.Minute)
	s := a.Begin()
	b.Invalidate(1)
	assert.True(t, a.Put(1, s, "a1"), "another cache's invalidation is not this one's")
	assert.True(t, a.Put(2, s, "a2"))
	a.Invalidate(1)
	_, ok := a.Get(1)
	assert.False(t, ok)
	got, ok := a.Get(2)
	assert.True(t, ok)
	assert.Equal(t, "a2", got)
}
