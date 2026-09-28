package db

import (
	"time"

	"gorm.io/gorm"
)

// SpentBillRetainedUntil returns the instant past which a Hub stops answering
// cash_status about a bill it destroyed, and whether a tombstone applies to
// that bill at all.
//
// Single definition on purpose. This policy is consulted from two packages —
// the NIP-47 handler deciding whether to answer, and the service layer
// deciding how long to keep the wallet's relay gate open — and when each had
// its own copy they drifted: one applied a floor the other did not, so a Hub
// configured below that floor kept its gate open while answering nothing.
//
// Returns ok=false when the bill is unknown, its Hub is gone, or retention is
// disabled (0). The deadline is measured from the spend, not from the bill's
// expiry, so a never-expiring bill is covered too.
//
// One statement. The deadline itself comes from the materialised
// CashBillArchive.RetainedUntil, while whether a policy exists at all is still
// read live, through the join — so disabling retention or deleting a Hub stops
// tombstones at once, with no column to keep in step for those two cases. What
// the join cannot see is a retention value that merely CHANGED, which is why
// apps.UpdateCashHubConfig recomputes the column.
//
// One indexed read of CashBillArchive.RetainedUntil, which is maintained at
// archive time and recomputed when a Hub's SpentRetentionSecs changes. It used
// to join cash_hub_configs and add the interval here, on every call; see that
// field for why that could not stay.
//
// No floor is applied here, deliberately: this exact instant is what the
// caller is told as retained_until, so honouring it is the honest thing. The
// post-delete grace that keeps an in-flight request reachable is a separate
// concern about the relay gate, applied at that gate.
func SpentBillRetainedUntil(tx *gorm.DB, walletPubkey string) (time.Time, bool) {
	if tx == nil || walletPubkey == "" {
		return time.Time{}, false
	}

	var retainedUntil *time.Time
	err := tx.Table("cash_bill_archives").
		Select("cash_bill_archives.retained_until").
		Joins("JOIN cash_hub_configs ON cash_hub_configs.app_id = cash_bill_archives.hub_app_id").
		Where("cash_bill_archives.wallet_pubkey = ?", walletPubkey).
		Where("cash_hub_configs.spent_retention_secs > 0").
		Limit(1).
		Scan(&retainedUntil).Error
	if err != nil || retainedUntil == nil {
		// No row, no hub config, retention disabled, or a row predating the
		// column — all of which mean no tombstone applies.
		return time.Time{}, false
	}

	return *retainedUntil, true
}

// RetentionWindowOpen is the ONE definition of the retention boundary: is a
// destroyed bill still inside the window ending at deadline?
//
// Inclusive of the deadline itself. That instant is quoted to the caller as
// retained_until, and "retained until T" answering at T is what the word says;
// a request landing exactly on it getting silence would contradict the figure
// the hub published.
//
// Exported, and taking plain instants rather than doing its own lookup, because
// it has two callers that reach the deadline differently: SpentBillStillAnswerable
// below, which only needs the verdict, and nip47's tombstone reply, which needs
// the deadline anyway to put in the response and so must not pay for a second
// query. Both MUST route their comparison through here.
//
// They used to each write the comparison out, and they disagreed: this one used
// !now.After(deadline) while the tombstone path used !now.Before(deadline), so at
// exactly T the relay gate admitted the request and the replier declined to
// answer it — the request was accepted and then silently dropped, which is the
// one outcome the tombstone exists to prevent. Aligning the two operators would
// have fixed that instant and left the duplication that produced it.
func RetentionWindowOpen(deadline, now time.Time) bool {
	return !now.After(deadline)
}

// SpentBillStillAnswerable reports whether a destroyed bill is still inside its
// Hub's retention window right now.
func SpentBillStillAnswerable(tx *gorm.DB, walletPubkey string, now time.Time) bool {
	deadline, ok := SpentBillRetainedUntil(tx, walletPubkey)
	return ok && RetentionWindowOpen(deadline, now)
}
