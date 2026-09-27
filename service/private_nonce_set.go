package service

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/flokiorg/lokihub/logger"
)

// nonceSetShards mirrors walletRegistry's striping, and for the same reason: an
// envelope nonce is uniform over the keyspace, so its first two hex characters
// pick a bucket with no hashing, and 256 buckets make lock contention negligible.
const nonceSetShards = 256

// nonceSetCapacity bounds how many nonces are remembered at once.
//
// Sized from the window, not guessed: a nonce need only be held until its
// envelope's not_after has passed, which is at most 120 s, and saturation is
// around 2k envelopes/s/core — so ~240k live entries. A million leaves an order
// of magnitude of headroom at roughly 40-60 MB.
//
// What happens at the cap is the important part. See seenOrRecord: the set FAILS
// CLOSED. Evicting to make room would silently reopen the replay window the set
// exists to close, which is the opposite of what a bound is for.
const nonceSetCapacity = 1 << 20

// nonceSweepInterval is how often expired nonces are reclaimed. Frequent enough
// that the set tracks the live window rather than growing toward the cap, and rare
// enough that the sweep itself is not a cost.
const nonceSweepInterval = 30 * time.Second

// privateNonceSet remembers the envelope nonces this hub has already accepted, so
// a captured envelope cannot be replayed.
//
// In memory rather than in the database, and that is a reasoned choice rather than
// a shortcut. Check what a replay could actually achieve on this transport:
// mint_cash is gated by CASH_HUB_SCOPE and lives on a Cash Hub app connection,
// which stays on kind 23194 — so it is not reachable here. Every method that is
// reachable (cash_status, cash_redeem, cash_transfer, cash_consolidate) is already
// guarded by bill state, so a replay finds the bill spent or the slice claimed and
// fails. And the replayer holds neither the ephemeral nor the inbox private key, so
// it cannot read the reply, which goes to the original reply_to regardless.
//
// So this set is a cheap denial-of-service mitigation, not a money-safety
// mechanism. A restart forgets it and reopens a window of at most 120 s in which an
// attacker can make the hub do redundant work — not one in which anything can be
// taken. That is why no durable write sits on this path.
type privateNonceSet struct {
	shards [nonceSetShards]privateNonceShard
	// count mirrors the total so Len is one atomic load. Kept in step under each
	// shard's lock, for the same reason walletRegistry does it: Len is passed as a
	// log field and Go evaluates that argument whether or not the level is enabled.
	count atomic.Int64
	// rejectedAtCapacity counts envelopes refused because the set was full. A
	// nonzero value means legitimate traffic is being turned away and the cap or
	// the sweep needs attention, so it must be visible rather than silent.
	rejectedAtCapacity atomic.Int64
}

type privateNonceShard struct {
	mu sync.Mutex
	// expiry maps a nonce to the unix second after which it may be forgotten —
	// the envelope's own not_after.
	expiry map[string]int64
}

func newPrivateNonceSet() *privateNonceSet {
	s := &privateNonceSet{}
	for i := range s.shards {
		s.shards[i].expiry = map[string]int64{}
	}
	return s
}

// shardFor picks a nonce's bucket from its first two hex characters, using the
// same hexNibble decoding walletRegistry does.
//
// Decoding matters: the characters are hex ASCII, so combining the raw bytes as
// (nonce[0]<<8 | nonce[1]) % 256 collapses to the second byte alone — and since
// that is one of only 16 hex digits, it would leave 240 of the 256 shards empty
// and concentrate all contention on the rest. TestNonceSet_ShardsSpreadEvenly
// exists because that is exactly the mistake this originally made.
func (s *privateNonceSet) shardFor(nonce string) *privateNonceShard {
	if len(nonce) < 2 {
		return &s.shards[0]
	}
	return &s.shards[int(hexNibble(nonce[0]))<<4|int(hexNibble(nonce[1]))]
}

// seenOrRecord atomically reports whether nonce has been seen, recording it if
// not. It returns true when the envelope is a replay and must be refused.
//
// One call rather than a separate check and insert, because two envelopes carrying
// the same nonce can arrive concurrently — the whole point is that exactly one of
// them proceeds, and a check-then-insert would let both through.
//
// At capacity it refuses the envelope instead of making room. Evicting an entry to
// admit a new one would reopen the replay window for whatever was evicted, so a
// full set means "cannot guarantee freshness" and the honest answer is to decline.
func (s *privateNonceSet) seenOrRecord(nonce string, notAfter int64) bool {
	shard := s.shardFor(nonce)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	if existing, ok := shard.expiry[nonce]; ok {
		// A known nonce whose window has already passed is not a replay worth
		// refusing on its own — the freshness check rejects a stale envelope
		// before it reaches here — but the entry is stale, so refresh it.
		if existing >= time.Now().Unix() {
			return true
		}
		shard.expiry[nonce] = notAfter
		return false
	}

	if s.count.Load() >= nonceSetCapacity {
		s.rejectedAtCapacity.Add(1)
		// Reported as a replay so the caller refuses the envelope. Distinguishing
		// the two would tell an attacker when the set is full, which is exactly
		// when they would want to know.
		return true
	}

	shard.expiry[nonce] = notAfter
	s.count.Add(1)
	return false
}

// Len is the number of nonces currently remembered.
func (s *privateNonceSet) Len() int64 { return s.count.Load() }

// sweepExpired drops nonces whose window has passed and returns how many went.
func (s *privateNonceSet) sweepExpired(now time.Time) int64 {
	cutoff := now.Unix()
	var removed int64

	for i := range s.shards {
		shard := &s.shards[i]
		shard.mu.Lock()
		for nonce, expiry := range shard.expiry {
			if expiry < cutoff {
				delete(shard.expiry, nonce)
				removed++
			}
		}
		shard.mu.Unlock()
	}

	if removed > 0 {
		s.count.Add(-removed)
	}
	return removed
}

// startNonceSweeper reclaims expired nonces on a timer so the set tracks the live
// window rather than growing toward its cap.
func (s *privateNonceSet) startNonceSweeper(done <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(nonceSweepInterval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				removed := s.sweepExpired(time.Now())
				if rejected := s.rejectedAtCapacity.Swap(0); rejected > 0 {
					// Legitimate traffic was refused. Loud on purpose: the
					// alternative to saying so is silently declining envelopes.
					logger.Logger.Warn().
						Int64("rejected", rejected).
						Int64("held", s.Len()).
						Int("capacity", nonceSetCapacity).
						Msg("Refused private transport envelopes because the replay-nonce set is full")
				}
				logger.Logger.Debug().
					Int64("removed", removed).
					Int64("held", s.Len()).
					Msg("Swept expired private transport nonces")
			}
		}
	}()
}
