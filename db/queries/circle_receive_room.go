package queries

import (
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// CircleReceiveRoomMloki is how much more a circle wallet may receive: its cap
// minus its balance and its invoices that can still be paid. capped is false
// for any other app, which has no receive cap.
func CircleReceiveRoomMloki(tx *gorm.DB, appID uint, now time.Time) (room int64, capped bool) {
	var circleApp db.App
	if tx.Where("id = ? AND parent_kind = ?", appID, db.ParentKindCircle).Limit(1).Find(&circleApp).RowsAffected == 0 {
		return 0, false
	}
	var capMloki int64
	tx.Model(&db.AppPermission{}).
		Where("app_id = ? AND scope = ?", appID, constants.MAKE_INVOICE_SCOPE).
		Pluck("max_amount_loki * 1000", &capMloki)
	if capMloki <= 0 {
		return 0, false
	}
	return capMloki - GetIsolatedBalance(tx, appID) - OutstandingIncomingMloki(tx, appID, now), true
}

// OutstandingIncomingMloki sums appID's incoming invoices that can still be paid.
// Expiry is compared in Go: the sqlite driver stores times in a form SQL date
// functions can't read.
func OutstandingIncomingMloki(tx *gorm.DB, appID uint, now time.Time) int64 {
	var pending []db.Transaction
	tx.Model(&db.Transaction{}).
		Select("amount_mloki", "expires_at").
		Where("app_id = ? AND type = ? AND state IN ?", appID, constants.TRANSACTION_TYPE_INCOMING,
			[]string{constants.TRANSACTION_STATE_PENDING, constants.TRANSACTION_STATE_ACCEPTED}).
		Find(&pending)
	var sum int64
	for _, t := range pending {
		if t.ExpiresAt == nil || t.ExpiresAt.After(now) {
			sum += int64(t.AmountMloki) //nolint:gosec // invoice amounts are far below int64 range
		}
	}
	return sum
}
