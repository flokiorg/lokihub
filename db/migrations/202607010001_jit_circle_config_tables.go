package migrations

import (
	"gorm.io/gorm"
)

// MigrateJITCircleConfigTables:
//  1. Creates jit_hub_configs and circle_hub_configs tables.
//  2. Renames circle_admin → circle_hub in apps.kind.
//  3. Promotes isolated apps that have the jit_hub scope to kind = jit_hub.
//  4. Backfills jit_hub_configs from old App columns (jit_per_wallet_max_mloki, jit_max_exp_secs).
//  5. Backfills circle_hub_configs from old App columns (circle_policy, provider_pubkey,
//     circle_max_exp_secs, circle_fees_ppm).
//  6. Drops the old feature columns from the apps table.
func MigrateJITCircleConfigTables(db *gorm.DB) error {
	if !db.Migrator().HasTable("apps") {
		return nil // fresh DB; AutoMigrate will create tables correctly
	}

	// Guard: skip if jit_hub_configs already exists (idempotent).
	if db.Migrator().HasTable("jit_hub_configs") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// -- 1. Create config tables ------------------------------------------------
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS jit_hub_configs (
			id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			app_id               INTEGER NOT NULL UNIQUE REFERENCES apps(id) ON DELETE CASCADE,
			per_wallet_max_mloki INTEGER,
			max_exp_secs         INTEGER
		)`).Error; err != nil {
			return err
		}

		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS circle_hub_configs (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			app_id          INTEGER NOT NULL UNIQUE REFERENCES apps(id) ON DELETE CASCADE,
			policy          TEXT,
			provider_pubkey TEXT,
			max_exp_secs    INTEGER,
			fees_ppm        INTEGER
		)`).Error; err != nil {
			return err
		}

		// -- 2. Rename circle_admin → circle_hub ------------------------------
		if err := tx.Exec(`UPDATE apps SET kind = 'circle_hub' WHERE kind = 'circle_admin'`).Error; err != nil {
			return err
		}

		// -- 3. Promote isolated JIT hubs ------------------------------------------
		// An isolated app that has a jit_hub AppPermission is a JIT Hub.
		if err := tx.Exec(`
			UPDATE apps SET kind = 'jit_hub'
			WHERE kind = 'isolated'
			  AND id IN (
			        SELECT app_id FROM app_permissions WHERE scope = 'jit_hub'
			      )
		`).Error; err != nil {
			return err
		}

		// -- 4. Backfill jit_hub_configs -------------------------------------------
		// Only attempt if the old columns exist.
		var hasJITCols int
		if err := tx.Raw(`SELECT COUNT(*) FROM pragma_table_info('apps') WHERE name='jit_per_wallet_max_mloki'`).Scan(&hasJITCols).Error; err != nil {
			return err
		}
		if hasJITCols > 0 {
			if err := tx.Exec(`
				INSERT INTO jit_hub_configs (app_id, per_wallet_max_mloki, max_exp_secs)
				SELECT id, jit_per_wallet_max_mloki, jit_max_exp_secs
				FROM apps
				WHERE kind = 'jit_hub' AND jit_per_wallet_max_mloki > 0
			`).Error; err != nil {
				return err
			}
		}

		// -- 5. Backfill circle_hub_configs -----------------------------------
		var hasCircleCols int
		if err := tx.Raw(`SELECT COUNT(*) FROM pragma_table_info('apps') WHERE name='circle_policy'`).Scan(&hasCircleCols).Error; err != nil {
			return err
		}
		if hasCircleCols > 0 {
			if err := tx.Exec(`
				INSERT INTO circle_hub_configs (app_id, policy, provider_pubkey, max_exp_secs, fees_ppm)
				SELECT id, circle_policy, provider_pubkey, circle_max_exp_secs, circle_fees_ppm
				FROM apps
				WHERE kind = 'circle_hub'
			`).Error; err != nil {
				return err
			}
		}

		// -- 6. Drop old feature columns -------------------------------------------
		oldCols := []string{
			"jit_per_wallet_max_mloki",
			"jit_max_exp_secs",
			"circle_policy",
			"provider_pubkey",
			"circle_max_exp_secs",
			"circle_fees_ppm",
		}
		for _, col := range oldCols {
			var count int
			if err := tx.Raw(`SELECT COUNT(*) FROM pragma_table_info('apps') WHERE name=?`, col).Scan(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				if err := tx.Exec(`ALTER TABLE apps DROP COLUMN ` + col).Error; err != nil {
					return err
				}
			}
		}

		return nil
	})
}
