package queries

import (
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"gorm.io/gorm"
)

// HasPendingOutgoing reports whether appId has a payment out that has not
// settled or failed yet. Deleting the app would cascade-delete that row, and its
// outcome (and a circle hub's fee credit) would then have nothing to update.
// It cannot pin a wallet forever: SweepStalePendingOutgoing resolves every
// pending outgoing row from the node.
func HasPendingOutgoing(tx *gorm.DB, appId uint) bool {
	var count int64
	tx.Model(&db.Transaction{}).
		Where("app_id = ? AND type = ? AND state = ?",
			appId, constants.TRANSACTION_TYPE_OUTGOING, constants.TRANSACTION_STATE_PENDING).
		Count(&count)
	return count > 0
}
