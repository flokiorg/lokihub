package migrations

import (
	"time"

	"gorm.io/gorm"
)

// retainedUntilBackfillBatch is how many archive rows one backfill pass reads
// and writes. Bounded because the table can hold millions of rows, and a single
// unbounded pass would hold locks and memory for the duration.
const retainedUntilBackfillBatch = 1000

// MigrateCashArchiveRetainedUntil populates CashBillArchive.RetainedUntil for
// rows that predate the column: ended_at plus the bill's Hub's
// spent_retention_secs, which is exactly what db.SpentBillRetainedUntil used to
// compute per read after a join. Materialising it turns the startup reload and
// the periodic sweep from 2N+1 statements into one indexed query each.
//
// Rows whose Hub is gone, or whose Hub has retention disabled, stay NULL — both
// already meant "no tombstone applies".
//
// The arithmetic is done in Go, deliberately, rather than in SQL. The sqlite
// driver stores a time.Time as Go's own String() rendering — including the
// monotonic-clock suffix, e.g. "2026-09-25 22:43:42.08 +0000 UTC
// m=-3599.93" — which sqlite's date functions cannot parse, so
// datetime(ended_at, '+N seconds') silently yields NULL. Reading the column back
// through gorm hands us a real time.Time on both dialects and cannot drift with
// the driver's storage format. This is a one-time cost, which is what makes that
// affordable here and not on the per-read path it replaces.
func MigrateCashArchiveRetainedUntil(db *gorm.DB) error {
	if !db.Migrator().HasTable("cash_bill_archives") || !db.Migrator().HasTable("cash_hub_configs") {
		return nil
	}
	if !db.Migrator().HasColumn("cash_bill_archives", "retained_until") {
		return nil
	}

	// Retention per Hub, read once: a Hub has far fewer rows than the archive
	// does, so this avoids a join per batch.
	type hubRetention struct {
		AppID              uint
		SpentRetentionSecs int
	}
	var hubs []hubRetention
	if err := db.Table("cash_hub_configs").
		Select("app_id, spent_retention_secs").
		Where("spent_retention_secs > 0").
		Scan(&hubs).Error; err != nil {
		return err
	}
	if len(hubs) == 0 {
		return nil
	}
	retentionByHub := make(map[uint]time.Duration, len(hubs))
	hubIDs := make([]uint, 0, len(hubs))
	for _, h := range hubs {
		retentionByHub[h.AppID] = time.Duration(h.SpentRetentionSecs) * time.Second
		hubIDs = append(hubIDs, h.AppID)
	}

	// Walked by primary key, so each pass makes progress whatever happens to an
	// individual row. A loop keyed on "still NULL" would spin forever on any row
	// the arithmetic could not fill, hanging startup.
	type archiveRow struct {
		ID       uint
		HubAppID uint
		EndedAt  time.Time
	}
	lastID := uint(0)
	for {
		var batch []archiveRow
		err := db.Table("cash_bill_archives").
			Select("id, hub_app_id, ended_at").
			Where("id > ? AND retained_until IS NULL AND hub_app_id IN ?", lastID, hubIDs).
			Order("id").
			Limit(retainedUntilBackfillBatch).
			Scan(&batch).Error
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}

		if err := db.Transaction(func(tx *gorm.DB) error {
			for _, row := range batch {
				retention, ok := retentionByHub[row.HubAppID]
				if !ok {
					continue
				}
				deadline := row.EndedAt.Add(retention)
				if err := tx.Table("cash_bill_archives").
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
