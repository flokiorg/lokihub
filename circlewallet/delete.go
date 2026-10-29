// Package circlewallet deletes Circle Wallets (NIP-CW §Lifecycle and
// Deletion, §Deleting a Circle Wallet): the one step that may move a member's
// dust to its Hub.
package circlewallet

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/ledger"
)

// ErrNotDeletable refuses a deletion: the wallet holds more than dust, or a
// payment to or from it is in flight.
var ErrNotDeletable = errors.New("circlewallet: the wallet holds more than dust or has a payment in flight")

// DeleteWallet deletes a wallet in tx, under its lock: it re-checks that the
// wallet holds at most its dust and has nothing in flight (ErrNotDeletable
// otherwise), fails its open invoices, moves the dust to its Hub, marks its join
// record and deletes it; its membership row cascades. The caller has cancelled
// its open invoices; after commit it cancels at the node the hold invoices this
// returns (one issued since), and publishes the deletion.
func DeleteWallet(tx *gorm.DB, wallet *db.App, now time.Time) (openHolds []string, err error) {
	if err := db.LockApp(tx, wallet.ID); err != nil {
		return nil, err
	}
	balance := queries.GetIsolatedBalance(tx, wallet.ID)
	inFlight, err := circlelife.InFlight(tx, wallet.ID)
	if err != nil {
		return nil, err
	}
	if inFlight || balance > circlelife.Dust(wallet) {
		return nil, ErrNotDeletable
	}
	// An open invoice left would have nowhere to settle.
	open := tx.Model(&db.Transaction{}).
		Where("app_id = ? AND type = ? AND state = ?", wallet.ID, constants.TRANSACTION_TYPE_INCOMING, constants.TRANSACTION_STATE_PENDING)
	if err := open.Session(&gorm.Session{}).Where("hold = ? AND self_payment = ?", true, false).
		Pluck("payment_hash", &openHolds).Error; err != nil {
		return nil, err
	}
	if err := open.Session(&gorm.Session{}).
		Updates(map[string]interface{}{"state": constants.TRANSACTION_STATE_FAILED, "failure_reason": "circle wallet deleted"}).Error; err != nil {
		return nil, err
	}
	if balance > 0 && wallet.ParentAppID != nil {
		if err := ledger.InternalTransferTx(tx, wallet.ID, *wallet.ParentAppID, uint64(balance), "circle wallet dust"); err != nil {
			return nil, fmt.Errorf("move the dust to the Hub: %w", err)
		}
	}
	if err := tx.Model(&db.CircleJoinRecord{}).Where("wallet_app_id = ?", wallet.ID).
		Update("wallet_deleted_at", now.Unix()).Error; err != nil {
		return nil, err
	}
	if err := tx.Where("wallet_app_id = ?", wallet.ID).Delete(&db.CircleWalletMembership{}).Error; err != nil {
		return nil, err
	}
	return openHolds, tx.Delete(&db.App{}, wallet.ID).Error
}
