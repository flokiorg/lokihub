package migrations

import (
	"gorm.io/gorm"
)

// MigrateRenameCircleChildToCircleWallet renames the stored kind value
// "circle_child" → "circle_wallet" in the apps table.
// The Go constant AppKindCircleChild was renamed to AppKindCircleWallet in the
// same changeset; this migration keeps the DB in sync with the new constant.
func MigrateRenameCircleChildToCircleWallet(db *gorm.DB) error {
	if !db.Migrator().HasTable("apps") {
		return nil // fresh DB; AutoMigrate will create the table with the new kind value
	}
	return db.Exec(`UPDATE apps SET kind = 'circle_wallet' WHERE kind = 'circle_child'`).Error
}
