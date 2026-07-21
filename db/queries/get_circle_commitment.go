package queries

import (
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"gorm.io/gorm"
)

// GetCircleCommitmentMloki returns the sum of max_amount_loki (converted to mloki) for all
// active circle_child wallets under parentAppID. Only counts wallets that have not expired.
// The caller (create_circle_wallet_controller.go) makes this check atomic with wallet
// creation by running both inside one transaction that takes a Postgres advisory lock
// (pg_advisory_xact_lock) keyed on parentAppID before calling this — pass that transaction's
// *gorm.DB as tx so the read is consistent with the lock.
func GetCircleCommitmentMloki(tx *gorm.DB, parentAppID uint) (int64, error) {
	var sum int64
	err := tx.Table("apps").
		Select("COALESCE(SUM(ap.max_amount_loki * 1000), 0)").
		Joins("JOIN app_permissions ap ON ap.app_id = apps.id AND ap.scope = ?", constants.PAY_INVOICE_SCOPE).
		Where("apps.parent_app_id = ?", parentAppID).
		Where("apps.parent_kind = ?", db.ParentKindCircle).
		Where("apps.expires_at > ? OR apps.expires_at IS NULL", time.Now()).
		Scan(&sum).Error
	return sum, err
}
