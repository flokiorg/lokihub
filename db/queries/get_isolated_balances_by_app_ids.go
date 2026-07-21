package queries

import (
	"github.com/flokiorg/lokihub/constants"
	"gorm.io/gorm"
)

// GetIsolatedBalancesByAppIDs returns each app's isolated balance (same formula as
// GetIsolatedBalance) for every given app ID in a single query. An app with no
// transactions has no entry in the result — callers should treat a missing key as
// a zero balance.
func GetIsolatedBalancesByAppIDs(tx *gorm.DB, appIDs []uint) (map[uint]int64, error) {
	if len(appIDs) == 0 {
		return map[uint]int64{}, nil
	}

	var rows []struct {
		AppId   uint
		Balance int64
	}
	err := tx.Table("transactions").
		Select(`app_id,
			COALESCE(SUM(CASE WHEN type = ? AND state = ? THEN amount_mloki ELSE 0 END), 0) -
			COALESCE(SUM(CASE WHEN type = ? AND (state = ? OR state = ?) THEN amount_mloki + fee_mloki + fee_reserve_mloki + fee_skim_mloki ELSE 0 END), 0)
			AS balance`,
			constants.TRANSACTION_TYPE_INCOMING, constants.TRANSACTION_STATE_SETTLED,
			constants.TRANSACTION_TYPE_OUTGOING, constants.TRANSACTION_STATE_SETTLED, constants.TRANSACTION_STATE_PENDING).
		Where("app_id IN ?", appIDs).
		Group("app_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	balances := make(map[uint]int64, len(rows))
	for _, r := range rows {
		balances[r.AppId] = r.Balance
	}
	return balances, nil
}
