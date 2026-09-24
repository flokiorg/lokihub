package service

import (
	"sync/atomic"
	"time"

	"github.com/flokiorg/lokihub/constants"
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
func (r *walletRegistry) RemoveAfterGrace(walletPubkey string) {
	r.RemoveAfter(walletPubkey, walletRetentionAfterDelete)
}

// RemoveAfter is RemoveAfterGrace with a caller-chosen window, for a destroyed
// cash bill whose hub still answers "spent" about it
// (db.CashHubConfig.SpentRetentionSecs). The gate has to stay open that long or
// the request never reaches the handler that would answer it.
//
// The timer is best-effort and deliberately not the source of truth: it is lost
// on restart (the registry is then reseeded from the archive), and an entry
// that outlives its window costs nothing, because the handler re-checks the
// deadline against the archive before answering. Erring toward keeping the
// gate open only ever means a request is let through to be met with silence —
// which is what it would have got anyway.
func (r *walletRegistry) RemoveAfter(walletPubkey string, d time.Duration) {
	if d <= 0 {
		r.Remove(walletPubkey)
		return
	}
	time.AfterFunc(d, func() {
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

// spentBillRetention is how long this wallet's pubkey must keep being served
// after its app row is gone: the hub's own spent-bill retention window when the
// deleted app was a cash bill, and the ordinary post-delete grace otherwise.
//
// Falls back to the grace on every unexpected shape — a missing archive row, a
// hub that is itself gone, retention disabled — so the worst case is today's
// behaviour rather than a wallet served forever.
func (svc *service) spentBillRetention(walletPubkey string) time.Duration {
	if svc.db == nil {
		return walletRetentionAfterDelete
	}
	var bill db.CashBillArchive
	if err := svc.db.Where("wallet_pubkey = ?", walletPubkey).First(&bill).Error; err != nil {
		return walletRetentionAfterDelete
	}
	var cfg db.CashHubConfig
	if err := svc.db.Where("app_id = ?", bill.HubAppID).First(&cfg).Error; err != nil {
		return walletRetentionAfterDelete
	}
	if cfg.SpentRetentionSecs <= 0 {
		return walletRetentionAfterDelete
	}
	remaining := time.Until(bill.EndedAt.Add(time.Duration(cfg.SpentRetentionSecs) * time.Second))
	if remaining < walletRetentionAfterDelete {
		return walletRetentionAfterDelete
	}
	return remaining
}

// retainedSpentBillPubkeys lists the wallet pubkeys of destroyed cash bills
// whose hub still answers "spent" about them, so a restart does not close a
// gate that has days left to run.
//
// The join is done in SQL rather than per row: a hub that has been minting for
// months can have a large archive, and only the rows still inside their own
// hub's window matter.
func (svc *service) retainedSpentBillPubkeys() []string {
	if svc.db == nil {
		return nil
	}
	var pubkeys []string
	err := svc.db.Model(&db.CashBillArchive{}).
		Joins("JOIN cash_hub_configs ON cash_hub_configs.app_id = cash_bill_archives.hub_app_id").
		Where("cash_hub_configs.spent_retention_secs > 0").
		Where("cash_bill_archives.ended_at > ?", time.Now().Add(-time.Duration(constants.MAX_EXPIRY_SECS)*time.Second)).
		Pluck("cash_bill_archives.wallet_pubkey", &pubkeys).Error
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to reload retained spent-bill wallets")
		return nil
	}
	// The per-hub deadline is re-checked in Go: expressing
	// ended_at + spent_retention_secs portably across sqlite and Postgres is
	// not worth a dialect branch here, and the handler re-checks it anyway.
	retained := pubkeys[:0]
	for _, pk := range pubkeys {
		if svc.spentBillRetention(pk) > walletRetentionAfterDelete {
			retained = append(retained, pk)
		}
	}
	return retained
}
