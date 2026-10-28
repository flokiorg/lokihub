package apps

import (
	"fmt"
	"github.com/flokiorg/lokihub/hubclock"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// The kinds of thing that keep a Cash Hub from being deleted (NIP-CASH §Hub
// Rules; Q23, XQ-22(1)).
const (
	CashHubBlockerLiveBills = "live_bills"
	CashHubBlockerRecords   = "records"
	// CashHubBlockerOrders: orders still retained, or with a payment not yet
	// minted (nipCO.OrderBlocksHubDelete).
	CashHubBlockerOrders = "orders"
	// CashHubBlockerPayoutOpen: on-chain payouts not yet final or cancelled;
	// CashHubBlockerWithdrawRecords: ended payouts whose records are still kept
	// (NIP-CASH §Lifecycle and Deletion, Hub teardown).
	CashHubBlockerPayoutOpen      = "payout_open"
	CashHubBlockerWithdrawRecords = "withdraw_records"
)

// CashHubDeleteBlockerFunc reports more things keeping a Cash Hub, read inside
// the delete's own transaction. Packages apps cannot import (the withdrawals)
// register one.
type CashHubDeleteBlockerFunc func(tx *gorm.DB, hubID uint, now time.Time) ([]CashHubDeleteBlocker, error)

var (
	extraBlockersMu sync.RWMutex
	extraBlockers   = map[string]CashHubDeleteBlockerFunc{}
)

// RegisterCashHubDeleteBlocker adds (or, under the same name, replaces) a
// blocker source every Cash Hub delete check reads.
func RegisterCashHubDeleteBlocker(kind string, f CashHubDeleteBlockerFunc) {
	extraBlockersMu.Lock()
	defer extraBlockersMu.Unlock()
	extraBlockers[kind] = f
}

// CashHubDeleteBlocker is one reason a Cash Hub cannot be deleted yet: how many,
// and until when (nil when no time applies: the bills must go first).
type CashHubDeleteBlocker struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
	Until *int64 `json:"until"`
}

// CashHubDeleteCheck is what deleting a Cash Hub waits for, and the earliest
// time it could go (nil when a blocker has no time).
type CashHubDeleteCheck struct {
	Blockers             []CashHubDeleteBlocker `json:"blockers"`
	EarliestDeleteAt     *int64                 `json:"earliest_delete_at"`
	WatchedAddresses     int64                  `json:"watched_addresses"`
	ConfirmationRequired bool                   `json:"confirmation_required"`
}

// Blocked reports whether anything keeps the Hub.
func (c CashHubDeleteCheck) Blocked() bool { return len(c.Blockers) > 0 }

// Message is the refusal a delete gives: each reason and the earliest time.
func (c CashHubDeleteCheck) Message() string {
	reasons := make([]string, 0, len(c.Blockers))
	for _, b := range c.Blockers {
		switch b.Kind {
		case CashHubBlockerLiveBills:
			reasons = append(reasons, fmt.Sprintf("%d live bill(s)", b.Count))
		case CashHubBlockerOrders:
			reasons = append(reasons, fmt.Sprintf("%d order(s) still open or awaiting their bills", b.Count))
		case CashHubBlockerPayoutOpen:
			reasons = append(reasons, fmt.Sprintf("%d on-chain payout(s) not yet final or cancelled", b.Count))
		case CashHubBlockerWithdrawRecords:
			if b.Until != nil {
				reasons = append(reasons, fmt.Sprintf("%d withdraw record(s) kept until %s", b.Count, time.Unix(*b.Until, 0).UTC().Format(time.RFC3339)))
			} else {
				reasons = append(reasons, fmt.Sprintf("%d withdraw record(s) kept while their re-minted bills live", b.Count))
			}
		case CashHubBlockerRecords:
			if b.Until != nil {
				reasons = append(reasons, fmt.Sprintf("%d request record(s) kept until %s", b.Count, time.Unix(*b.Until, 0).UTC().Format(time.RFC3339)))
			} else {
				reasons = append(reasons, fmt.Sprintf("%d request record(s) kept until their bills are gone", b.Count))
			}
		default:
			reasons = append(reasons, fmt.Sprintf("%d %s", b.Count, b.Kind))
		}
	}
	when := "after the bills are gone"
	if c.EarliestDeleteAt != nil {
		when = "at " + time.Unix(*c.EarliestDeleteAt, 0).UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("this Cash Hub cannot be deleted yet: %s; the earliest it can go is %s", strings.Join(reasons, ", "), when)
}

// CashHubDeleteCheckTx reads what keeps a Cash Hub at now, inside tx: its live
// bills, and every request record that names it and is still retained (a held
// record is within retention: records drop only at drop_after). The insert of
// every record naming the Hub runs under the Hub's lock, as the delete does, so
// nothing appears between this check and the delete.
func CashHubDeleteCheckTx(tx *gorm.DB, hubID uint, now time.Time) (CashHubDeleteCheck, error) {
	check := CashHubDeleteCheck{Blockers: []CashHubDeleteBlocker{}}
	var bills int64
	if err := tx.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", hubID, db.AppKindCashWallet).Count(&bills).Error; err != nil {
		return check, err
	}
	if bills > 0 {
		check.Blockers = append(check.Blockers, CashHubDeleteBlocker{Kind: CashHubBlockerLiveBills, Count: bills})
	}
	var records []db.CashRequestRecord
	// Order and withdraw records are kept by their own rules and reported by
	// their own blockers (orders below; withdrawals registered by cashwithdraw),
	// with their real times: counting them here too named them "kept until
	// their bills are gone" with no time at all.
	if err := tx.Select("id", "drop_after").Where("hub_app_id = ? AND method NOT IN ?", hubID,
		[]string{constants.NIP47MethodCashOrder, constants.NIP47MethodCashWithdraw}).
		Find(&records).Error; err != nil {
		return check, err
	}
	if len(records) > 0 {
		blocker := CashHubDeleteBlocker{Kind: CashHubBlockerRecords, Count: int64(len(records))}
		var latest int64
		timed := true
		for _, r := range records {
			if r.DropAfter == nil {
				timed = false
				break
			}
			latest = max(latest, *r.DropAfter)
		}
		if timed {
			blocker.Until = &latest
		}
		check.Blockers = append(check.Blockers, blocker)
	}
	h, err := cashrequest.HorizonTx(tx)
	if err != nil {
		return check, err
	}
	orders, at, timed, err := HubOrderBlockersTx(tx, hubID, h, now)
	if err != nil {
		return check, err
	}
	if orders > 0 {
		blocker := CashHubDeleteBlocker{Kind: CashHubBlockerOrders, Count: orders}
		if timed {
			blocker.Until = &at
		}
		check.Blockers = append(check.Blockers, blocker)
	}
	extraBlockersMu.RLock()
	kinds := make([]string, 0, len(extraBlockers))
	for k := range extraBlockers {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	fns := make([]CashHubDeleteBlockerFunc, len(kinds))
	for i, k := range kinds {
		fns[i] = extraBlockers[k]
	}
	extraBlockersMu.RUnlock()
	for _, f := range fns {
		bs, err := f(tx, hubID, now)
		if err != nil {
			return check, err
		}
		for _, b := range bs {
			if b.Count > 0 {
				check.Blockers = append(check.Blockers, b)
			}
		}
	}
	// Addresses of on-chain orders that saw a payment are watched for as long
	// as the Hub runs; deleting it ends the watch (NIP-CO §On-chain).
	if err := tx.Model(&db.CashOrder{}).Where("hub_app_id = ? AND payment = ? AND state IN ?", hubID, "onchain",
		[]string{db.CashOrderPaid, db.CashOrderMinted, db.CashOrderClosed}).Count(&check.WatchedAddresses).Error; err != nil {
		return check, err
	}
	check.ConfirmationRequired = check.WatchedAddresses > 0
	check.EarliestDeleteAt = earliestDelete(check.Blockers, now)
	return check, nil
}

// earliestDelete is the latest of the blockers' times, or nil when one has none.
func earliestDelete(blockers []CashHubDeleteBlocker, now time.Time) *int64 {
	if len(blockers) == 0 {
		at := now.Unix()
		return &at
	}
	var latest int64
	for _, b := range blockers {
		if b.Until == nil {
			return nil
		}
		latest = max(latest, *b.Until)
	}
	return &latest
}

// CashHubDeleteCheck is CashHubDeleteCheckTx outside a transaction, for the
// delete-check endpoint.
func (svc *appsService) CashHubDeleteCheck(hubID uint) (CashHubDeleteCheck, error) {
	return CashHubDeleteCheckTx(svc.db, hubID, hubclock.Now())
}
