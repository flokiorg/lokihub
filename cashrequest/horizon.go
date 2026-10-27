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
// the stored one is ignored, and the caller logs it.
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
		return tx.Save(&db.CashHorizon{ID: horizonRowID, HSecs: h, UpdatedAt: now.Unix()}).Error
	})
	return h, err
}
