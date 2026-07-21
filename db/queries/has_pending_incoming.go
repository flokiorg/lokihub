package queries

import (
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"gorm.io/gorm"
)

// HasPendingIncoming reports whether appId has any incoming transaction still
// awaiting settlement. Used to defer destructive cleanup of a sub-wallet until
// no payment could still be in flight to it.
func HasPendingIncoming(tx *gorm.DB, appId uint) bool {
	var count int64
	tx.Model(&db.Transaction{}).
		Where("app_id = ? AND type = ? AND state = ?",
			appId, constants.TRANSACTION_TYPE_INCOMING, constants.TRANSACTION_STATE_PENDING).
		Count(&count)
	return count > 0
}
