package service

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// walletRetentionAfterDelete is how long a deleted app's wallet keeps being
// served before it is dropped from the registry.
//
// Not zero, deliberately. A request that arrives just after its app is
// deleted must still reach HandleEvent, which answers it with a proper NIP-47
// error ("no slice registered for this identity"). Dropping the wallet the
// instant the app goes means that request is silently discarded and the
// caller waits out its full 30s deadline instead — a hang where there used to
// be an error, which the cash split/spin-off flows depend on and which is
// simply worse for a real client whose bill was just spent.
//
// The per-wallet subscriptions this replaced had the same window by accident:
// cancelling a subscription only stops delivery once the relay processes the
// CLOSE, so requests kept arriving for a moment afterwards. This makes that
// window explicit and bounded rather than a race that happened to fall the
// right way.
const walletRetentionAfterDelete = time.Minute

// walletRegistryShards is how many independent buckets the registry is split
// into. A pubkey's first two hex characters pick one, which spreads evenly
// because wallet pubkeys are uniform over the keyspace — no hashing needed.
//
// 256 is the natural fit for one hex byte, and it makes lock contention
// negligible: a writer holds one bucket for the couple of hundred nanoseconds a
// map insert takes, so the chance of a reader meeting it is remote.
const walletRegistryShards = 256

// walletRegistry is the set of app wallet pubkeys this hub serves.
//
// With config.TrustedNwcRelay on, the hub holds a single subscription for all
// NIP-47 requests rather than one per wallet, so this set is what decides
// whether an inbound event is ours. It is therefore read once per event,
// including every junk event an attacker can publish to the relay, and written
// when an app is created or deleted.
//
// Sharded maps behind a mutex each, rather than one copy-on-write map behind an
// atomic pointer. The copy-on-write version gave readers a lock-free probe, but
// every write rebuilt the whole set: measured at 817 ms and 112 MB of garbage
// per write once the registry held 3M wallets, which a node serving a few
// million-bill hubs does. Since the registry grows with bills in circulation
// rather than with users, "write-rare" stopped being true.
//
// Benched against sharded copy-on-write, sync.Map, raw [32]byte keys and a
// Bloom filter (service/wallet_registry_candidates_bench_test.go). This shape
// takes a write from 817 ms to ~300 ns with no allocation at all, and costs the
// read path about 3 ns — the copy disappears rather than merely shrinking. The
// property given up is that a reader never blocks behind a writer; with 256
// shards that becomes 1/256 of readers possibly waiting a few hundred
// nanoseconds, which is not worth 817 ms.
type walletRegistry struct {
	shards [walletRegistryShards]walletRegistryShard
	// count mirrors the total across shards so Len is a single atomic load.
	//
	// Not cosmetic: acceptsRequestEvent's discard log passes Len() as a field,
	// and Go evaluates that argument whether or not debug logging is enabled —
	// so Len runs for every junk event an attacker publishes. Summing 256
	// shards under their read locks there measured 1.3 us per rejected event,
	// against ~20 ns for the whole gate. Kept in step under each shard's lock.
	count atomic.Int64
}

type walletRegistryShard struct {
	mu      sync.RWMutex
	pubkeys map[string]struct{}
}

func newWalletRegistry() *walletRegistry {
	r := &walletRegistry{}
	for i := range r.shards {
		r.shards[i].pubkeys = map[string]struct{}{}
	}
	return r
}

// shardFor picks a wallet's bucket from the first two hex characters of its
// pubkey. A pubkey too short to have them is not one this hub ever registered,
// so bucket 0 is as good as any — Has will miss there either way.
func (r *walletRegistry) shardFor(walletPubkey string) *walletRegistryShard {
	if len(walletPubkey) < 2 {
		return &r.shards[0]
	}
	return &r.shards[int(hexNibble(walletPubkey[0]))<<4|int(hexNibble(walletPubkey[1]))]
}

// hexNibble maps a hex digit to its value, and anything else to 0. Malformed
// input only lands in the wrong bucket, where it will not be found; the gate
// already rejects it on length.
func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

// Has reports whether walletPubkey belongs to an app this hub serves. This is
// the hot path: one shard lookup, a read lock, one map probe, no allocation.
func (r *walletRegistry) Has(walletPubkey string) bool {
	shard := r.shardFor(walletPubkey)
	shard.mu.RLock()
	_, ok := shard.pubkeys[walletPubkey]
	shard.mu.RUnlock()
	return ok
}

// Add registers wallet pubkeys. Adding one already present is a no-op, so
// callers can re-add freely (a resubscribe, a replayed event).
//
// Grouped by shard so a bulk add — startup loads the whole set this way — takes
// each lock once rather than once per pubkey.
func (r *walletRegistry) Add(walletPubkeys ...string) {
	added := int64(0)
	for _, pk := range walletPubkeys {
		shard := r.shardFor(pk)
		shard.mu.Lock()
		if _, exists := shard.pubkeys[pk]; !exists {
			shard.pubkeys[pk] = struct{}{}
			added++
		}
		shard.mu.Unlock()
	}
	if added > 0 {
		r.count.Add(added)
	}
}

// RemoveAfterGrace stops serving a wallet once walletRetentionAfterDelete has
// passed, so a request racing its app's deletion still gets an error response
// rather than silence. See that constant for why the delay exists.
//
// Only for wallets with nothing left to answer. A destroyed cash bill still
// inside its hub's retention window is deliberately NOT scheduled here: its
// deadline lives in the archive (CashBillArchive.EndedAt), and a timer would
// be a weaker second copy of that state — lost on restart, and unreasonable to
// hold open at 15-day horizons. The periodic sweep prunes those instead
// (service.PruneExpiredSpentBills).
func (r *walletRegistry) RemoveAfterGrace(walletPubkey string) {
	time.AfterFunc(walletRetentionAfterDelete, func() {
		logger.Logger.Debug().
			Str("wallet", walletPubkey).
			Msg("No longer serving a deleted app's wallet")
		r.Remove(walletPubkey)
	})
}

// Remove deregisters wallet pubkeys immediately.
//
// Variadic to mirror Add. With in-place maps a removal is no longer a copy, but
// the batching still matters: it takes each shard's lock once for the whole
// batch instead of once per pubkey, and PruneExpiredSpentBills can hand over
// hundreds at a time.
func (r *walletRegistry) Remove(walletPubkeys ...string) {
	removed := int64(0)
	for _, pk := range walletPubkeys {
		shard := r.shardFor(pk)
		shard.mu.Lock()
		if _, exists := shard.pubkeys[pk]; exists {
			delete(shard.pubkeys, pk)
			removed++
		}
		shard.mu.Unlock()
	}
	if removed > 0 {
		r.count.Add(-removed)
	}
}

// Len reports how many wallets are registered.
//
// One atomic load, because this IS on the event path: the gate's discard log
// passes it as a field, and its arguments are evaluated even when that log is
// suppressed. See walletRegistry.count.
func (r *walletRegistry) Len() int {
	return int(r.count.Load())
}

// spentBillRetained reports whether walletPubkey belongs to a destroyed cash
// bill its hub still answers "spent" about.
//
// The archive is the only source of truth: CashBillArchive.EndedAt plus the
// hub's SpentRetentionSecs. Nothing about the window is held in memory, so a
// restart resumes it exactly where it left off.
//
// Deliberately NOT floored by walletRetentionAfterDelete. That grace is about
// the relay gate — keeping an in-flight request reachable — and is applied at
// the gate, by the RemoveAfterGrace branch a false answer here falls back to.
// Conflating the two is what let this function and the handler disagree about
// the same deadline, so the answering window now has exactly one definition
// (db.SpentBillRetainedUntil), which is also the retained_until a caller is
// told.
func (svc *service) spentBillRetained(walletPubkey string) bool {
	return db.SpentBillStillAnswerable(svc.db, walletPubkey, time.Now())
}

// retainedSpentBillPubkeys lists the wallet pubkeys of destroyed cash bills
// whose hub still answers "spent" about them — used to rebuild the set on
// startup, since the window lives in the archive rather than in memory.
//
// One statement, whatever the archive holds. It used to pluck every candidate
// and then call spentBillRetained per row, which is two more queries each:
// 2N+1 in total, measured at 100,001 statements and 15.6 s on postgres for 50k
// archived bills, all of it before the hub answers anything. A request arriving
// in that window meets silence, which a caller cannot distinguish from a spent
// bill or a dead hub — the exact ambiguity the tombstone exists to remove.
//
// The deadline comes from the materialised retained_until; whether a policy
// exists at all still comes from the join, so a hub whose retention was disabled
// or whose config is gone drops out live. Same shape as db.SpentBillRetainedUntil,
// deliberately: one definition of "still answerable", expressed once in SQL.
func (svc *service) retainedSpentBillPubkeys() []string {
	if svc.db == nil {
		return nil
	}
	var pubkeys []string
	err := svc.db.Table("cash_bill_archives").
		Joins("JOIN cash_hub_configs ON cash_hub_configs.app_id = cash_bill_archives.hub_app_id").
		Where("cash_hub_configs.spent_retention_secs > 0").
		// >= not >, to match db.RetentionWindowOpen's !now.After(deadline): the window
		// is open THROUGH the deadline instant. With > this gate closed one second
		// before the replier stopped answering, so at exactly retained_until the bill
		// was unregistered while still contractually answerable — the caller got
		// silence at the one instant the tombstone exists to cover.
		Where("cash_bill_archives.retained_until >= ?", time.Now()).
		Pluck("cash_bill_archives.wallet_pubkey", &pubkeys).Error
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to reload retained spent-bill wallets")
		return nil
	}
	return pubkeys
}

// pruneExpiredSpentBillsBatch is how many expired wallets one pass deregisters.
// Bounded for the same reason the cash cleanup sweep is: a tick should do a
// known amount of work, not however much has accumulated.
const pruneExpiredSpentBillsBatch = 500

// PruneExpiredSpentBills drops registry entries whose retention window has
// passed. Called from the periodic cash sweep, so the set converges on the
// archive without any long-lived timer.
//
// Bounded and indexed: it asks only for wallets that have actually expired,
// rather than reading every archive row and re-deriving each one's deadline. The
// old shape was 2N+1 statements per tick — 100,001 and 26.2 s on postgres at 50k
// archived bills, extrapolating past the five-minute tick interval at a million,
// so the sweep could never finish before the next one began.
func (svc *service) PruneExpiredSpentBills() {
	if svc.db == nil || svc.walletRegistry == nil {
		return
	}

	var expired []string
	err := svc.db.Table("cash_bill_archives").
		// < not <=, the mirror of the gate above: a bill is reclaimable only once the
		// window has actually closed, and at exactly retained_until it has not.
		Where("retained_until IS NOT NULL AND retained_until < ?", time.Now()).
		Limit(pruneExpiredSpentBillsBatch).
		Pluck("wallet_pubkey", &expired).Error
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to query expired spent-bill wallets")
		return
	}
	if len(expired) == 0 {
		return
	}

	// One call, so the registry is touched once for the whole batch.
	svc.walletRegistry.Remove(expired...)
}
