package migrations

import (
	"gorm.io/gorm"
)

// MigrateAppKind backfills the new `kind` column from the old `isolated` bool,
// then drops the `isolated` column. New columns added to the App struct
// (parent_app_id, parent_kind, etc.) are handled by AutoMigrate.
// SQLite 3.35.0+ (2021) is required for ALTER TABLE DROP COLUMN.
func MigrateAppKind(db *gorm.DB) error {
	if !db.Migrator().HasTable("apps") {
		return nil // fresh DB; AutoMigrate will create the table correctly
	}

	// Check whether the old column still exists.
	var isolatedCount int
	if err := db.Raw("SELECT COUNT(*) FROM pragma_table_info('apps') WHERE name='isolated'").Scan(&isolatedCount).Error; err != nil {
		return err
	}
	if isolatedCount == 0 {
		return nil // already migrated
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// Add kind column if it doesn't exist yet.
		var kindCount int
		if err := tx.Raw("SELECT COUNT(*) FROM pragma_table_info('apps') WHERE name='kind'").Scan(&kindCount).Error; err != nil {
			return err
		}
		if kindCount == 0 {
			if err := tx.Exec("ALTER TABLE apps ADD COLUMN kind TEXT NOT NULL DEFAULT 'standard'").Error; err != nil {
				return err
			}
		}

		// Backfill from isolated bool.
		if err := tx.Exec("UPDATE apps SET kind = 'isolated' WHERE isolated = 1").Error; err != nil {
			return err
		}

		// Drop the old column (requires SQLite ≥ 3.35.0).
		if err := tx.Exec("ALTER TABLE apps DROP COLUMN isolated").Error; err != nil {
			return err
		}

		return nil
	})
}
