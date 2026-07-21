package migrations

import (
	"time"

	"gorm.io/gorm"
)

// MigrateCircleIdentityAllowlistTimestamps adds created_at to
// circle_identity_allowed_pubkeys so "last policy update" can be reported for
// allowlist-policy CircleIdentities (see CircleIdentitySummaryWithCounts).
// AutoMigrate alone would add the column but leave existing rows at Go's zero
// time, which would render as "updated 2000+ years ago" — so existing rows
// are explicitly backfilled to now() here, before AutoMigrate ever runs.
func MigrateCircleIdentityAllowlistTimestamps(db *gorm.DB) error {
	if !db.Migrator().HasTable("circle_identity_allowed_pubkeys") {
		return nil // fresh DB; AutoMigrate will create the column correctly
	}

	var hasCreatedAtCol int
	if err := db.Raw(`SELECT COUNT(*) FROM pragma_table_info('circle_identity_allowed_pubkeys') WHERE name='created_at'`).Scan(&hasCreatedAtCol).Error; err != nil {
		return err
	}
	if hasCreatedAtCol > 0 {
		return nil // already migrated
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`ALTER TABLE circle_identity_allowed_pubkeys ADD COLUMN created_at TIMESTAMP`).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE circle_identity_allowed_pubkeys SET created_at = ? WHERE created_at IS NULL`, time.Now()).Error
	})
}
