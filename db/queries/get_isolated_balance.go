package queries

import (
	"github.com/flokiorg/lokihub/constants"
	"gorm.io/gorm"
)

func GetIsolatedBalance(tx *gorm.DB, appId uint) int64 {
	var received struct {
		Sum int64
	}
	tx.
		Table("transactions").
		Select("SUM(amount_mloki) as sum").
		Where("app_id = ? AND type = ? AND state = ?", appId, constants.TRANSACTION_TYPE_INCOMING, constants.TRANSACTION_STATE_SETTLED).Scan(&received)

	var spent struct {
		Sum int64
	}

	tx.
		Table("transactions").
		Select("SUM(amount_mloki + fee_mloki + fee_reserve_mloki) as sum").
		Where("app_id = ? AND type = ? AND (state = ? OR state = ?)", appId, constants.TRANSACTION_TYPE_OUTGOING, constants.TRANSACTION_STATE_SETTLED, constants.TRANSACTION_STATE_PENDING).Scan(&spent)

	return received.Sum - spent.Sum
}
