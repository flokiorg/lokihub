package migrations

import (
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/logger"
)

// MigrateAppsAppPubkeyUnique makes apps.app_pubkey unique at the database level.
//
// Every incoming NWC request is routed to an app by its app_pubkey, so two apps
// must never share one. prepareApp refuses a duplicate, but that is a read before
// the insert: two concurrent creations (on postgres, joins to different circle
// hubs hold different advisory locks) could both pass it. The index makes the
// second insert fail, and saveAppTx maps that failure to ErrDuplicateAppPubkey.
//
// Partial (non-empty app_pubkey only), so a row without a key can never block the index;
// the column is NOT NULL and every code path sets it, so this excludes nothing
// today. Same syntax on sqlite and postgres.
//
// A database that already holds duplicates can't take the index. Creating it
// would fail every startup, so the duplicates are logged for the operator and
// the index is skipped; prepareApp's check still guards new apps, and the next
// startup after the duplicates are resolved creates it.
func MigrateAppsAppPubkeyUnique(db *gorm.DB) error {
	if !db.Migrator().HasTable("apps") {
		return nil
	}

	var duplicates []struct {
		AppPubkey string
		Count     int
	}
	if err := db.Table("apps").
		Select("app_pubkey, COUNT(*) AS count").
		Where("app_pubkey <> ''").
		Group("app_pubkey").
		Having("COUNT(*) > 1").
		Scan(&duplicates).Error; err != nil {
		return err
	}
	if len(duplicates) > 0 {
		for _, d := range duplicates {
			logger.Logger.Error().Str("app_pubkey", d.AppPubkey).Int("apps", d.Count).
				Msg("Several apps share one app_pubkey; not creating the unique index on apps.app_pubkey until this is resolved")
		}
		return nil
	}

	return db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_apps_app_pubkey_unique
		ON apps (app_pubkey)
		WHERE app_pubkey <> ''
	`).Error
}
