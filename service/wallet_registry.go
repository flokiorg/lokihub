package service

import (
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

// Remove deregisters a wallet pubkey immediately.
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
func (svc *service) retainedSpentBillPubkeys() []string {
	if svc.db == nil {
		return nil
	}
	var candidates []string
	err := svc.db.Model(&db.CashBillArchive{}).
		Joins("JOIN cash_hub_configs ON cash_hub_configs.app_id = cash_bill_archives.hub_app_id").
		Where("cash_hub_configs.spent_retention_secs > 0").
		Pluck("cash_bill_archives.wallet_pubkey", &candidates).Error
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to reload retained spent-bill wallets")
		return nil
	}
	// The per-hub deadline is re-checked in Go rather than in SQL: expressing
	// ended_at + spent_retention_secs portably across sqlite and Postgres is
	// not worth a dialect branch in a money-adjacent query.
	retained := candidates[:0]
	for _, pk := range candidates {
		if svc.spentBillRetained(pk) {
			retained = append(retained, pk)
		}
	}
	return retained
}

// PruneExpiredSpentBills drops registry entries whose retention window has
// passed. Called from the periodic cash sweep, so the set converges on the
// archive without any long-lived timer.
func (svc *service) PruneExpiredSpentBills() {
	if svc.db == nil || svc.walletRegistry == nil {
		return
	}
	var candidates []string
	if err := svc.db.Model(&db.CashBillArchive{}).
		Pluck("wallet_pubkey", &candidates).Error; err != nil {
		return
	}
	for _, pk := range candidates {
		if !svc.spentBillRetained(pk) {
			svc.walletRegistry.Remove(pk)
		}
	}
}
