package migrations

import (
	"gorm.io/gorm"
)

// MigrateCircleIdentityTables extracts Circle "who's allowed" (policy +
// provider pubkey + allowlist) out of circle_hub_configs into a
// standalone, reusable circle_identities table that has no FK back to any
// App — deleting every circle_hub that references an identity leaves the
// identity (and its allowlist) intact, and multiple circle_hub apps may
// reference the same identity concurrently.
//
//  1. Creates circle_identities and circle_identity_allowed_pubkeys.
//  2. Adds circle_hub_configs.circle_identity_id.
//  3. Backfills one CircleIdentity per existing circle_hub_configs row
//     (pre-prod: no dedup across rows, a 1:1 backfill is correct and simplest).
//  4. Migrates circle_allowed_pubkeys rows into circle_identity_allowed_pubkeys,
//     resolved through the now-backfilled circle_identity_id.
//  5. Drops the old policy/provider_pubkey columns and the old allowlist table.
func MigrateCircleIdentityTables(db *gorm.DB) error {
	if !db.Migrator().HasTable("circle_hub_configs") {
		return nil // fresh DB; AutoMigrate will create tables correctly
	}

	// Guard: skip if circle_identities already exists (idempotent).
	if db.Migrator().HasTable("circle_identities") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// -- 1. Create the new tables ----------------------------------------
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS circle_identities (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			name            TEXT NOT NULL,
			policy          TEXT,
			provider_pubkey TEXT
		)`).Error; err != nil {
			return err
		}

		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS circle_identity_allowed_pubkeys (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			circle_identity_id INTEGER NOT NULL REFERENCES circle_identities(id) ON DELETE CASCADE,
			pubkey             TEXT NOT NULL
		)`).Error; err != nil {
			return err
		}

		// -- 2. Add circle_identity_id to circle_hub_configs ------------
		var hasIdentityIDCol int
		if err := tx.Raw(`SELECT COUNT(*) FROM pragma_table_info('circle_hub_configs') WHERE name='circle_identity_id'`).Scan(&hasIdentityIDCol).Error; err != nil {
			return err
		}
		if hasIdentityIDCol == 0 {
			if err := tx.Exec(`ALTER TABLE circle_hub_configs ADD COLUMN circle_identity_id INTEGER`).Error; err != nil {
				return err
			}
		}

		// -- 3. Backfill: one CircleIdentity per existing config row ---------
		// Only attempt if the old columns still exist (idempotency / fresh-ish DBs).
		var hasPolicyCol int
		if err := tx.Raw(`SELECT COUNT(*) FROM pragma_table_info('circle_hub_configs') WHERE name='policy'`).Scan(&hasPolicyCol).Error; err != nil {
			return err
		}
		if hasPolicyCol > 0 {
			type legacyConfig struct {
				ID             uint
				Name           string
				Policy         string
				ProviderPubkey string
			}
			var configs []legacyConfig
			rows, err := tx.Raw(`
				SELECT cpc.id, a.name, cpc.policy, cpc.provider_pubkey
				FROM circle_hub_configs cpc
				JOIN apps a ON a.id = cpc.app_id
			`).Rows()
			if err != nil {
				return err
			}
			for rows.Next() {
				var c legacyConfig
				if err := rows.Scan(&c.ID, &c.Name, &c.Policy, &c.ProviderPubkey); err != nil {
					rows.Close()
					return err
				}
				configs = append(configs, c)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()

			for _, c := range configs {
				if err := tx.Exec(`INSERT INTO circle_identities (name, policy, provider_pubkey) VALUES (?, ?, ?)`,
					c.Name, c.Policy, c.ProviderPubkey).Error; err != nil {
					return err
				}
				var newIdentityID uint
				if err := tx.Raw(`SELECT last_insert_rowid()`).Scan(&newIdentityID).Error; err != nil {
					return err
				}
				if err := tx.Exec(`UPDATE circle_hub_configs SET circle_identity_id = ? WHERE id = ?`,
					newIdentityID, c.ID).Error; err != nil {
					return err
				}
			}

			// -- 4. Migrate the old allowlist rows, resolved through the
			// circle_identity_id we just backfilled. Use tx (not the outer db)
			// for this check — querying the outer connection from inside an
			// in-flight SQLite transaction contends with tx's lock and can
			// stall for the full busy_timeout instead of returning immediately.
			if tx.Migrator().HasTable("circle_allowed_pubkeys") {
				if err := tx.Exec(`
					INSERT INTO circle_identity_allowed_pubkeys (circle_identity_id, pubkey)
					SELECT cpc.circle_identity_id, cap.pubkey
					FROM circle_allowed_pubkeys cap
					JOIN circle_hub_configs cpc ON cpc.app_id = cap.app_id
				`).Error; err != nil {
					return err
				}
			}
		}

		// -- 5. Drop old columns/table ----------------------------------------
		for _, col := range []string{"policy", "provider_pubkey"} {
			var count int
			if err := tx.Raw(`SELECT COUNT(*) FROM pragma_table_info('circle_hub_configs') WHERE name=?`, col).Scan(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				if err := tx.Exec(`ALTER TABLE circle_hub_configs DROP COLUMN ` + col).Error; err != nil {
					return err
				}
			}
		}

		if tx.Migrator().HasTable("circle_allowed_pubkeys") {
			if err := tx.Exec(`DROP TABLE circle_allowed_pubkeys`).Error; err != nil {
				return err
			}
		}

		return nil
	})
}
