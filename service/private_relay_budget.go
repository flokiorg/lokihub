package service

import (
	"sync"
	"time"
)

// relayEnvelopeBudget is a token bucket per relay connection for the envelopes
// the hub decrypts (NIP-CASH §Serving an Envelope: a Hub MAY rate-limit, per
// relay connection, before E1). An event over the budget is dropped undecrypted.
type relayEnvelopeBudget struct {
	perMin float64
	mu     sync.Mutex
	relays map[string]*relayBucket
}

type relayBucket struct {
	tokens float64
	last   time.Time
}

// newRelayEnvelopeBudget allows perMin envelopes a minute per relay, with a
// burst of one minute's worth; nil (no limit) when perMin is not positive.
func newRelayEnvelopeBudget(perMin int) *relayEnvelopeBudget {
	if perMin <= 0 {
		return nil
	}
	return &relayEnvelopeBudget{perMin: float64(perMin), relays: map[string]*relayBucket{}}
}

func (b *relayEnvelopeBudget) allow(relay string, now time.Time) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	bucket, ok := b.relays[relay]
	if !ok {
		bucket = &relayBucket{tokens: b.perMin, last: now}
		b.relays[relay] = bucket
	}
	bucket.tokens = min(b.perMin, bucket.tokens+now.Sub(bucket.last).Minutes()*b.perMin)
	bucket.last = now
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}
