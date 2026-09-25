package migrations

import (
	"time"

	"gorm.io/gorm"

	dbpkg "github.com/flokiorg/lokihub/db"
)

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

	// Per Hub, reusing the same helper a live retention change uses, so the
	// backfilled value and the recomputed one can never be computed two
	// different ways. onlyMissing, since this fills a new column rather than
	// applying a policy change.
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

	for _, hub := range hubs {
		retention := time.Duration(hub.SpentRetentionSecs) * time.Second
		if err := dbpkg.RecomputeSpentRetention(db, hub.AppID, retention, true); err != nil {
			return err
		}
	}

	return nil
}
