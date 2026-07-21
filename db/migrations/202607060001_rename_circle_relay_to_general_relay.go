package migrations

import (
	"gorm.io/gorm"
)

// MigrateRenameCircleRelayToGeneralRelay renames the stored config key
// "CircleRelay" → "GeneralRelay" in user_configs. The Go config accessors
// GetCircleRelay/SetCircleRelay/GetCircleRelayUrls were renamed to their
// GeneralRelay equivalents in the same changeset; this migration carries
// forward any relay list an existing install already had configured under
// the old key.
func MigrateRenameCircleRelayToGeneralRelay(db *gorm.DB) error {
	if !db.Migrator().HasTable("user_configs") {
		return nil // fresh DB; the new key is seeded directly under "GeneralRelay"
	}

	var generalRelayCount int64
	if err := db.Table("user_configs").Where("key = ?", "GeneralRelay").Count(&generalRelayCount).Error; err != nil {
		return err
	}
	if generalRelayCount > 0 {
		// "GeneralRelay" was already seeded (e.g. a newer build ran against this
		// DB before this migration existed). Drop the stale "CircleRelay" row
		// rather than renaming into it, which would violate the unique key.
		return db.Exec(`DELETE FROM user_configs WHERE key = 'CircleRelay'`).Error
	}

	return db.Exec(`UPDATE user_configs SET key = 'GeneralRelay' WHERE key = 'CircleRelay'`).Error
}
