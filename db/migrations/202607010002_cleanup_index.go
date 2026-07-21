package migrations

import (
	"gorm.io/gorm"
)

// MigrateCleanupIndex adds a partial index on apps(expires_at) covering only
// sub-wallet rows that are pending cleanup. This makes the JIT cleanup sweep
// O(log expired) instead of O(log total) regardless of how many apps exist.
func MigrateCleanupIndex(db *gorm.DB) error {
	if !db.Migrator().HasTable("apps") {
		return nil
	}

	return db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_apps_cleanup
		ON apps (expires_at)
		WHERE parent_app_id IS NOT NULL AND cleanup_in_progress = false
	`).Error
}
