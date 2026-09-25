package db

import (
	"time"

	"gorm.io/gorm"
)

// retentionRecomputeBatch is how many archive rows one recompute pass touches.
// Bounded because a Hub's archive can hold millions of rows and this runs inside
// an operator's request.
const retentionRecomputeBatch = 1000

// RecomputeSpentRetention rewrites CashBillArchive.RetainedUntil for one Hub's
// destroyed bills, from a retention value that has just changed.
//
// This is what keeps the materialised column honest: the window is policy, and a
// Hub that changes SpentRetentionSecs must have that apply to bills it already
// destroyed, not only to future ones. Lowering it therefore shortens deadlines
// already quoted to callers as retained_until, and raising it can make an
// already-expired bill answerable again — both deliberate, both the consequence
// of retention being live rather than frozen at destruction time.
//
// retention of 0 clears the column, since that means "no tombstone".
//
// onlyMissing limits the pass to rows with no deadline yet, which is what the
// backfill migration wants; a policy change passes false and rewrites all of the
// Hub's rows.
//
// The arithmetic is done in Go rather than SQL for the reason
// SpentBillRetainedUntil's column documents: the sqlite driver stores a
// time.Time as Go's String() rendering, which sqlite's date functions cannot
// parse, so datetime(ended_at, '+N seconds') silently yields NULL.
func RecomputeSpentRetention(tx *gorm.DB, hubAppID uint, retention time.Duration, onlyMissing bool) error {
	if tx == nil || hubAppID == 0 {
		return nil
	}

	if retention <= 0 {
		if onlyMissing {
			// Nothing to fill: no retention means no deadline.
			return nil
		}
		return tx.Model(&CashBillArchive{}).
			Where("hub_app_id = ? AND retained_until IS NOT NULL", hubAppID).
			Update("retained_until", nil).Error
	}

	type archiveRow struct {
		ID      uint
		EndedAt time.Time
	}

	// Walked by primary key so every pass makes progress. Keying the loop on
	// "rows that still need it" would spin forever on any row a pass failed to
	// fill.
	lastID := uint(0)
	for {
		query := tx.Table("cash_bill_archives").
			Select("id, ended_at").
			Where("hub_app_id = ? AND id > ?", hubAppID, lastID)
		if onlyMissing {
			query = query.Where("retained_until IS NULL")
		}

		var batch []archiveRow
		if err := query.Order("id").Limit(retentionRecomputeBatch).Scan(&batch).Error; err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}

		if err := tx.Transaction(func(inner *gorm.DB) error {
			for _, row := range batch {
				deadline := row.EndedAt.Add(retention)
				if err := inner.Table("cash_bill_archives").
					Where("id = ?", row.ID).
					Update("retained_until", deadline).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}

		lastID = batch[len(batch)-1].ID
	}
}
