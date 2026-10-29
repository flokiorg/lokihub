package getinfocache

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A result computed before an invalidation is not stored after it; one computed
// after is, and is answered until the next invalidation.
func TestCache_ResultComputedBeforeAnInvalidationIsNotStored(t *testing.T) {
	c := &Cache[string]{}
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

func TestCache_KeysAreIndependentAndCachesSeparate(t *testing.T) {
	a, b := &Cache[uint]{}, &Cache[uint]{}
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
