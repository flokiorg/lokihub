package service

import "sync/atomic"

// walletRegistry is the set of app wallet pubkeys this hub serves.
//
// With config.TrustedNwcRelay on, the hub holds a single subscription for all
// NIP-47 requests rather than one per wallet, so this set is what decides
// whether an inbound event is ours. It is therefore read once per event,
// including every junk event an attacker can publish to the relay, and
// written only when an app is created or deleted.
//
// That read-heavy, write-rare shape is why it is copy-on-write behind an
// atomic pointer rather than a mutex or sync.Map: readers take no lock, never
// block behind a write, and cost one map probe. Writers pay a full copy,
// which is fine at app-creation rates and keeps the hot path free of
// contention when hundreds of cash wallets exist.
type walletRegistry struct {
	pubkeys atomic.Pointer[map[string]struct{}]
}

func newWalletRegistry() *walletRegistry {
	r := &walletRegistry{}
	empty := map[string]struct{}{}
	r.pubkeys.Store(&empty)
	return r
}

// Has reports whether walletPubkey belongs to an app this hub serves. This is
// the hot path: no locks, no allocation.
func (r *walletRegistry) Has(walletPubkey string) bool {
	current := r.pubkeys.Load()
	if current == nil {
		return false
	}
	_, ok := (*current)[walletPubkey]
	return ok
}

// Add registers wallet pubkeys. Adding one already present is a no-op, so
// callers can re-add freely (a resubscribe, a replayed event).
func (r *walletRegistry) Add(walletPubkeys ...string) {
	for {
		current := r.pubkeys.Load()

		missing := false
		for _, pk := range walletPubkeys {
			if _, exists := (*current)[pk]; !exists {
				missing = true
				break
			}
		}
		if !missing {
			return
		}

		next := make(map[string]struct{}, len(*current)+len(walletPubkeys))
		for pk := range *current {
			next[pk] = struct{}{}
		}
		for _, pk := range walletPubkeys {
			next[pk] = struct{}{}
		}

		// CompareAndSwap rather than Store: two app creations can race here,
		// and a plain store would drop whichever copy was built first —
		// losing a wallet from the set means silently ignoring its requests.
		if r.pubkeys.CompareAndSwap(current, &next) {
			return
		}
	}
}

// Remove deregisters a wallet pubkey, as when its app is deleted.
func (r *walletRegistry) Remove(walletPubkey string) {
	for {
		current := r.pubkeys.Load()
		if _, exists := (*current)[walletPubkey]; !exists {
			return
		}

		next := make(map[string]struct{}, len(*current))
		for pk := range *current {
			if pk != walletPubkey {
				next[pk] = struct{}{}
			}
		}

		if r.pubkeys.CompareAndSwap(current, &next) {
			return
		}
	}
}

// Len reports how many wallets are registered. For logging and tests only —
// not on the event path.
func (r *walletRegistry) Len() int {
	current := r.pubkeys.Load()
	if current == nil {
		return 0
	}
	return len(*current)
}
