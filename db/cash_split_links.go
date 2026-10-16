package db

import (
	"time"

	"gorm.io/gorm"
)

// ClearArchivedSplitLinks forgets which bill an archived slice's value moved
// into, for slices archived before cutoff, and returns how many it cleared.
//
// A consolidation's links are a standing statement that its source bills shared
// a holder (NIP-CASH §Privacy Considerations), so they are kept only for the
// configured window. Everything else on the row stays: amount, outcome and dates
// are what accounting needs, and the stats count splits by outcome, not by link.
func ClearArchivedSplitLinks(tx *gorm.DB, cutoff time.Time) (int64, error) {
	res := tx.Model(&CashBillSliceArchive{}).
		Where("spun_off_to_wallet_app_id IS NOT NULL AND archived_at < ?", cutoff).
		Update("spun_off_to_wallet_app_id", nil)
	return res.RowsAffected, res.Error
}
