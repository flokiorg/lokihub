package queries

import (
	"github.com/flokiorg/lokihub/constants"
	"gorm.io/gorm"
)

// balanceChunkSize is how many app IDs go into one IN clause.
//
// Every ID binds as its own SQL variable, and both drivers cap how many a
// statement may carry — roughly 32k on sqlite (SQLITE_MAX_VARIABLE_NUMBER) and
// 65k on postgres. Passing the whole list is therefore not merely slow past
// that point, it fails outright with "too many SQL variables", which took out
// the Cash Hub dashboard, the Circle Hub stats and the children listing for any
// provider with more children than the ceiling.
//
// 1000 is well under the lower of the two limits, leaves room for the five
// bound values in the SELECT, and keeps the statement count to
// ceil(len(appIDs)/1000) — bounded and small, rather than growing per row as a
// naive per-app loop would.
const balanceChunkSize = 1000

// GetIsolatedBalancesByAppIDs returns each app's isolated balance (same formula as
// GetIsolatedBalance) for every given app ID. An app with no transactions has no
// entry in the result — callers should treat a missing key as a zero balance.
//
// Issued as one statement per balanceChunkSize app IDs, so it stays correct for
// a provider of any size. Callers may pass as many IDs as they like.
func GetIsolatedBalancesByAppIDs(tx *gorm.DB, appIDs []uint) (map[uint]int64, error) {
	if len(appIDs) == 0 {
		return map[uint]int64{}, nil
	}

	balances := make(map[uint]int64, len(appIDs))

	for start := 0; start < len(appIDs); start += balanceChunkSize {
		end := min(start+balanceChunkSize, len(appIDs))

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
			Where("app_id IN ?", appIDs[start:end]).
			Group("app_id").
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}

		for _, r := range rows {
			balances[r.AppId] = r.Balance
		}
	}

	return balances, nil
}
