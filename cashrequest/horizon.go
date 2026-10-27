package cashrequest

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// horizonRowID is the only row of cash_horizon.
const horizonRowID = 1

// HorizonTx is the stored horizon H, or 0 when none was stored yet.
func HorizonTx(tx *gorm.DB) (int64, error) {
	var row db.CashHorizon
	err := tx.First(&row, horizonRowID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return row.HSecs, err
}

// RaiseHorizon stores configured as H unless a larger H is stored, and returns the
// H now in force. H never decreases (NIP-CASH §Deadlines): a configured value below
// the stored one is ignored, and the caller logs it. Raising H moves every
// record's drop time to at least deadline + H + 1.
func RaiseHorizon(gormDB *gorm.DB, configured int64, now time.Time) (int64, error) {
	var h int64
	err := gormDB.Transaction(func(tx *gorm.DB) error {
		stored, err := HorizonTx(tx)
		if err != nil {
			return err
		}
		h = max(stored, configured)
		if h == stored {
			return nil
		}
		if err := tx.Save(&db.CashHorizon{ID: horizonRowID, HSecs: h, UpdatedAt: now.Unix()}).Error; err != nil {
			return err
		}
		// A record's drop time was fixed with the H in force when it was set. A
		// lookup now decides with the new H, so no record may go before
		// deadline + H: otherwise a committed call could be answered unknown
		// (NIP-CASH §The Record, Retention).
		return tx.Model(&db.CashRequestRecord{}).
			Where("drop_after IS NOT NULL AND drop_after < deadline + ? + 1 AND method NOT IN ?", h, ownedElsewhere).
			Update("drop_after", gorm.Expr("deadline + ? + 1", h)).Error
	})
	return h, err
}
