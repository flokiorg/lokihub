package queries

import (
	"github.com/flokiorg/lokihub/constants"
	"gorm.io/gorm"
)

func GetIsolatedBalance(tx *gorm.DB, appId uint) int64 {
	var result struct {
		Balance int64
	}
	tx.Raw(`
		SELECT
			COALESCE(SUM(CASE WHEN type = ? AND state = ? THEN amount_mloki ELSE 0 END), 0) -
			COALESCE(SUM(CASE WHEN type = ? AND (state = ? OR state = ?) THEN amount_mloki + fee_mloki + fee_reserve_mloki ELSE 0 END), 0)
		AS balance
		FROM transactions
		WHERE app_id = ?`,
		constants.TRANSACTION_TYPE_INCOMING, constants.TRANSACTION_STATE_SETTLED,
		constants.TRANSACTION_TYPE_OUTGOING, constants.TRANSACTION_STATE_SETTLED, constants.TRANSACTION_STATE_PENDING,
		appId,
	).Scan(&result)
	return result.Balance
}
