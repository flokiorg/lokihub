package cashrequest

import (
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// HorizonTx is the stored horizon H, or 0 when none was stored yet. The store
// itself lives in db so that config can read it without importing the
// cash domain.
func HorizonTx(tx *gorm.DB) (int64, error) { return db.HorizonTx(tx) }

// RaiseHorizon stores configured as H unless a larger H is stored (see
// db.RaiseHorizon) and returns the H now in force.
func RaiseHorizon(gormDB *gorm.DB, configured int64, now time.Time) (int64, error) {
	return db.RaiseHorizon(gormDB, configured, now)
}
