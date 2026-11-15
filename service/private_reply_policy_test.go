package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// U-9.5: a reply's created_at lies in [now − 172800, now], and is not constant.
func TestReplyCreatedAt_BackdatedUniformly(t *testing.T) {
	now := time.Now()
	seen := map[int64]bool{}
	for i := 0; i < 1000; i++ {
		ts := int64(replyCreatedAt(now))
		assert.LessOrEqual(t, ts, now.Unix())
		assert.GreaterOrEqual(t, ts, now.Unix()-replyBackdateMax)
		seen[ts] = true
	}
	assert.Greater(t, len(seen), 900, "created_at must be random")
}

// U-9.8: the per-relay budget drops what exceeds it, per relay, and refills.
func TestRelayEnvelopeBudget(t *testing.T) {
	b := newRelayEnvelopeBudget(3)
	now := time.Now()
	for i := 0; i < 3; i++ {
		assert.True(t, b.allow("wss://a", now))
	}
	assert.False(t, b.allow("wss://a", now))
	assert.True(t, b.allow("wss://b", now), "another relay has its own budget")
	assert.True(t, b.allow("wss://a", now.Add(30*time.Second)), "the bucket refills")
	assert.Nil(t, newRelayEnvelopeBudget(0))
	assert.True(t, (*relayEnvelopeBudget)(nil).allow("wss://a", now))
}
