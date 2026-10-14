package cashwallet

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/archive"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/events"
)

// AutoDeleteDrained is the one auto-delete policy for a drained bill (NIP-CASH
// §Lifecycle and Deletion: the same for every drain mechanism). It archives and
// deletes walletAppID if it is a cash_wallet with no unclaimed slice, a zero
// balance and no pending incoming payment, and reports whether it did. Any
// other app, or a bill still holding value, is left alone.
//
// The drain guard runs in the same transaction as the archive and the delete,
// so a concurrent rollback cannot restore an unclaimed slice between check and
// delete. The deletion event is published after the commit, so the consumer's
// own lookup cannot race the transaction; it is what drops the wallet from the
// registry.
func AutoDeleteDrained(gormDB *gorm.DB, eventPublisher events.EventPublisher, walletAppID uint) (bool, error) {
	var app db.App
	if err := gormDB.Where("id = ? AND kind = ?", walletAppID, db.AppKindCashWallet).Limit(1).Find(&app).Error; err != nil {
		return false, err
	}
	if app.ID == 0 {
		// Not a cash_wallet, or already gone.
		return false, nil
	}

	err := gormDB.Transaction(func(tx *gorm.DB) error {
		return archive.ArchiveAndDeleteDrainedCashBillTx(tx, &app, archive.DrainGuards{
			IsolatedBalance:    queries.GetIsolatedBalance,
			HasPendingIncoming: queries.HasPendingIncoming,
		}, time.Now())
	})
	if errors.Is(err, archive.ErrBillNotDrained) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	eventPublisher.Publish(&events.Event{
		Event: "nwc_app_deleted",
		Properties: map[string]interface{}{
			"name": app.Name,
			"id":   app.ID,
		},
	})
	return true, nil
}
