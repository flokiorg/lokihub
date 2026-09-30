package queries

import (
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"gorm.io/gorm"
)

// expiredInvoiceSettlementGrace is how long past its own expiry a PENDING
// incoming invoice is still treated as possibly-settling.
//
// An expired invoice cannot be paid, so in principle zero would do. The grace
// exists for the gap between a payment being accepted and this database hearing
// about it: an HTLC accepted moments before expiry settles afterwards, and the
// notification that moves the row out of PENDING can lag. The two error
// directions are not symmetric — too short risks deleting a wallet a settlement
// is about to credit, which loses funds; too long only means a drained wallet
// takes longer to reclaim, and the callers retry — so this is deliberately far
// longer than any plausible lag. It matches cashTransferProofRetention in
// service/cash_cleanup_service.go, for the same "well beyond the real window"
// reason.
const expiredInvoiceSettlementGrace = time.Hour

// HasPendingIncoming reports whether appId has an incoming payment that could
// still settle. Used to defer destructive cleanup of a sub-wallet until no
// payment could still be in flight to it: deleting the app cascade-deletes the
// transaction row, and a settlement would then have nowhere to credit.
//
// Two states count, and one of them used not to:
//
//   - PENDING is an invoice awaiting payment. It counts only while it could
//     still be paid. An invoice past its own expiry can NEVER settle, so
//     counting it deferred cleanup FOREVER: nothing in this codebase moves a
//     PENDING incoming row to FAILED when its invoice lapses, so the caller's
//     "try again shortly" was never going to become true, and the wallet, its
//     parent hub and (for a circle) its identity were permanently unreclaimable.
//
//     Found live on the dev backend as 27 circle_hub/circle_wallet pairs plus
//     their 27 identities. Every one traced to a test that created an invoice on
//     a circle wallet and then failed before paying it, leaving a 24-hour
//     invoice behind; teardown's delete was refused and only logged, and nothing
//     ever retried. This fix does not stop a lapsed invoice being left behind —
//     it makes the resulting pin self-healing instead of permanent, so the same
//     wallet becomes reclaimable once its invoice has lapsed by the grace below.
//   - ACCEPTED is a hold invoice whose HTLC is committed and held. That is a
//     payment genuinely in flight — it will credit on settle — and it was not
//     counted at all, which defeated this guard for exactly the case it exists
//     to protect. Hold invoices are wire-reachable: make_hold_invoice maps to
//     MAKE_INVOICE_SCOPE (nip47/permissions/permissions.go), which cash hubs
//     and circle wallets are created with. An ACCEPTED row counts regardless of
//     the invoice's expiry, because an accepted HTLC must be settled or
//     cancelled either way; expiry has no bearing on it once it is held.
//
// The expiry comparison is done in Go, not in SQL. This project's sqlite driver
// stores a time.Time as Go's own String() rendering, which SQL date functions
// do not understand — they return NULL, silently, so a WHERE clause comparing
// expires_at would filter nothing and look like it worked.
func HasPendingIncoming(tx *gorm.DB, appId uint) bool {
	var inFlight []db.Transaction
	tx.Model(&db.Transaction{}).
		Select("state", "expires_at").
		Where("app_id = ? AND type = ? AND state IN ?",
			appId, constants.TRANSACTION_TYPE_INCOMING,
			[]string{constants.TRANSACTION_STATE_PENDING, constants.TRANSACTION_STATE_ACCEPTED}).
		Find(&inFlight)

	cutoff := time.Now().Add(-expiredInvoiceSettlementGrace)
	for _, transaction := range inFlight {
		if transaction.State == constants.TRANSACTION_STATE_ACCEPTED {
			return true
		}
		// A nil expiry means no deadline is known, so it is treated as still
		// live. The guard protects funds; an unknown is not a reason to delete.
		if transaction.ExpiresAt == nil || transaction.ExpiresAt.After(cutoff) {
			return true
		}
	}
	return false
}
