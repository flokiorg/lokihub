package db

import (
	"errors"
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
// No floor is applied here, deliberately: this exact instant is what the
// caller is told as retained_until, so honouring it is the honest thing. The
// post-delete grace that keeps an in-flight request reachable is a separate
// concern about the relay gate, applied at that gate.
func SpentBillRetainedUntil(tx *gorm.DB, walletPubkey string) (time.Time, bool) {
	if tx == nil || walletPubkey == "" {
		return time.Time{}, false
	}

	var bill CashBillArchive
	if err := tx.Where("wallet_pubkey = ?", walletPubkey).First(&bill).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return time.Time{}, false
		}
		return time.Time{}, false
	}

	var cfg CashHubConfig
	if err := tx.Where("app_id = ?", bill.HubAppID).First(&cfg).Error; err != nil {
		// The Hub itself is gone, so there is no retention policy to honour.
		return time.Time{}, false
	}
	if cfg.SpentRetentionSecs <= 0 {
		return time.Time{}, false
	}

	return bill.EndedAt.Add(time.Duration(cfg.SpentRetentionSecs) * time.Second), true
}

// SpentBillStillAnswerable reports whether a destroyed bill is still inside its
// Hub's retention window right now.
func SpentBillStillAnswerable(tx *gorm.DB, walletPubkey string, now time.Time) bool {
	deadline, ok := SpentBillRetainedUntil(tx, walletPubkey)
	return ok && now.Before(deadline)
}
