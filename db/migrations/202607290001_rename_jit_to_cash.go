package migrations

import (
	"gorm.io/gorm"
)

// MigrateRenameJITToCash renames the "JIT Wallet" feature to "Cash Hub" /
// "cash_wallet" everywhere it's stored, and adds the columns the new
// partial-split capability needs:
//  1. Renames jit_hub_configs → cash_hub_configs, adding min_transfer_mloki
//     and max_transfers.
//  2. Renames jit_wallet_claims → cash_wallet_claims, adding
//     min_transfer_mloki (existing rows default to 0/no-floor — the feature
//     didn't exist for them).
//  3. Renames the unique index on the claims table to match the new name.
//  4. Updates stored apps.kind ("jit_hub"→"cash_hub", "jit_wallet"→
//     "cash_wallet") and apps.parent_kind ("jit"→"cash") values.
//  5. Adds apps.split_from_wallet_app_id (nullable, informational lineage
//     column for the new split operation).
//  6. Rewrites existing app_permissions.scope values ("jit_hub"→"cash_hub",
//     "jit_claim_funds"→"cash_redeem", "jit_transfer"→"cash_transfer") — this
//     step is required, not cosmetic: without it, every already-granted
//     permission on an existing cash_hub/cash_wallet references a scope
//     string no NWC method checks for anymore, breaking every live
//     connection the instant this ships.
//
// The top-level fast path checks columns this migration is responsible for
// adding (not just table presence) — deliberately re-checked on every new
// revision of this function, so a DB that already went through an EARLIER
// revision still picks up a LATER revision's new idempotent step instead of
// being skipped wholesale by a stale "table exists" check alone. It
// deliberately does NOT check max_transfers (added by this migration
// originally, then permanently dropped by the later
// MigrateDropCashMaxTransfers) — a column a subsequent migration removes can
// never be part of a reliable "has this migration's own work already
// happened" signal, since its absence would otherwise look identical to
// "never migrated" on every DB that's gone through both migrations, causing
// this one to redundantly re-attempt (and fail on) a RENAME that already
// happened. min_transfer_mloki has no such later migration touching it, so it
// alone remains a safe, permanent signal.
//
// This fast path is also load-bearing for a separate, unrelated reason: it
// protects against a re-entrancy quirk in MigrateJITCircleConfigTables
// (earlier in the migration pipeline, historical, not modified here). That
// migration's own idempotency check (HasTable("jit_hub_configs")) can't
// distinguish "never migrated" from "already migrated past the point where
// jit_hub_configs stopped existing" — so if Migrate() somehow runs twice
// against a DB that's already fully renamed (observed in this repo's own
// test suite via SQLite's "file::memory:?cache=shared" mode letting two
// separate test functions' Migrate() calls share one persistent in-memory
// DB), it can recreate an empty jit_hub_configs table AFTER this migration
// already renamed it away — which would otherwise make the RENAME below
// collide with the cash_hub_configs table that stale recreation never
// touches. Bailing out here, before ever reaching that RENAME, sidesteps it
// entirely whenever this migration's own work is already fully done.
func MigrateRenameJITToCash(db *gorm.DB) error {
	if !db.Migrator().HasTable("apps") {
		return nil // fresh DB; AutoMigrate will create tables with the new names/columns directly
	}
	if db.Migrator().HasTable("cash_hub_configs") &&
		db.Migrator().HasColumn("cash_hub_configs", "min_transfer_mloki") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// -- 1. Rename jit_hub_configs → cash_hub_configs -------------------
		// Checked against tx, not the outer db handle: a HasTable check
		// against a different connection/session from inside this
		// transaction isn't guaranteed to see this same transaction's own
		// view of the schema consistently.
		if tx.Migrator().HasTable("jit_hub_configs") {
			if err := tx.Exec(`ALTER TABLE jit_hub_configs RENAME TO cash_hub_configs`).Error; err != nil {
				return err
			}
		} else {
			// No prior JIT config table (e.g. a DB that never had a JIT hub) —
			// create the new one directly so AutoMigrate has something to add
			// columns to rather than create-from-scratch racing this migration.
			if err := tx.Exec(`CREATE TABLE IF NOT EXISTS cash_hub_configs (
				id                   INTEGER PRIMARY KEY AUTOINCREMENT,
				app_id               INTEGER NOT NULL UNIQUE REFERENCES apps(id) ON DELETE CASCADE,
				per_wallet_max_mloki INTEGER,
				max_exp_secs         INTEGER
			)`).Error; err != nil {
				return err
			}
		}
		var hasMinTransferHub int
		if err := tx.Raw(`SELECT COUNT(*) FROM pragma_table_info('cash_hub_configs') WHERE name='min_transfer_mloki'`).Scan(&hasMinTransferHub).Error; err != nil {
			return err
		}
		if hasMinTransferHub == 0 {
			if err := tx.Exec(`ALTER TABLE cash_hub_configs ADD COLUMN min_transfer_mloki INTEGER NOT NULL DEFAULT 0`).Error; err != nil {
				return err
			}
		}
		// max_transfers moved from a caller-supplied, per-mint_cash-
		// call value to a hub-level setting (mirroring min_transfer_mloki) —
		// existing hubs default to 0 (unlimited), matching what "omitted"
		// already meant on every mint_cash call before this column
		// existed, so no hub's effective policy changes on upgrade.
		var hasMaxTransfersHub int
		if err := tx.Raw(`SELECT COUNT(*) FROM pragma_table_info('cash_hub_configs') WHERE name='max_transfers'`).Scan(&hasMaxTransfersHub).Error; err != nil {
			return err
		}
		if hasMaxTransfersHub == 0 {
			if err := tx.Exec(`ALTER TABLE cash_hub_configs ADD COLUMN max_transfers INTEGER NOT NULL DEFAULT 0`).Error; err != nil {
				return err
			}
		}

		// -- 2. Rename jit_wallet_claims → cash_wallet_claims ---------------
		if tx.Migrator().HasTable("jit_wallet_claims") {
			if err := tx.Exec(`ALTER TABLE jit_wallet_claims RENAME TO cash_wallet_claims`).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Exec(`CREATE TABLE IF NOT EXISTS cash_wallet_claims (
				id                       INTEGER PRIMARY KEY AUTOINCREMENT,
				wallet_app_id            INTEGER NOT NULL,
				identity_type            TEXT NOT NULL,
				identity_value           TEXT NOT NULL,
				ia_pubkey                TEXT,
				amount_mloki             INTEGER NOT NULL,
				claimed_at               DATETIME,
				max_transfers            INTEGER,
				transfer_count           INTEGER,
				spun_off_to_wallet_app_id INTEGER,
				created_at               DATETIME
			)`).Error; err != nil {
				return err
			}
		}
		var hasMinTransferClaim int
		if err := tx.Raw(`SELECT COUNT(*) FROM pragma_table_info('cash_wallet_claims') WHERE name='min_transfer_mloki'`).Scan(&hasMinTransferClaim).Error; err != nil {
			return err
		}
		if hasMinTransferClaim == 0 {
			if err := tx.Exec(`ALTER TABLE cash_wallet_claims ADD COLUMN min_transfer_mloki INTEGER NOT NULL DEFAULT 0`).Error; err != nil {
				return err
			}
		}

		// -- 3. Rename the unique index to match ----------------------------
		// SQLite doesn't rename an index when its table is renamed via
		// ALTER TABLE ... RENAME TO — it keeps its original name attached to
		// the renamed table. Drop it explicitly; GORM's AutoMigrate recreates
		// it under the new name (idx_cash_claim_wallet_identity) from the
		// CashWalletClaim struct tags right after this migration runs.
		if err := tx.Exec(`DROP INDEX IF EXISTS idx_jit_claim_wallet_identity`).Error; err != nil {
			return err
		}

		// -- 4. Rename stored kind values ------------------------------------
		if err := tx.Exec(`UPDATE apps SET kind = 'cash_hub' WHERE kind = 'jit_hub'`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE apps SET kind = 'cash_wallet' WHERE kind = 'jit_wallet'`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE apps SET parent_kind = 'cash' WHERE parent_kind = 'jit'`).Error; err != nil {
			return err
		}

		// -- 5. Add the new split lineage column -----------------------------
		var hasSplitFrom int
		if err := tx.Raw(`SELECT COUNT(*) FROM pragma_table_info('apps') WHERE name='split_from_wallet_app_id'`).Scan(&hasSplitFrom).Error; err != nil {
			return err
		}
		if hasSplitFrom == 0 {
			if err := tx.Exec(`ALTER TABLE apps ADD COLUMN split_from_wallet_app_id INTEGER REFERENCES apps(id)`).Error; err != nil {
				return err
			}
		}

		// -- 6. Rewrite existing granted scope strings -----------------------
		if err := tx.Exec(`UPDATE app_permissions SET scope = 'cash_hub' WHERE scope = 'jit_hub'`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE app_permissions SET scope = 'cash_redeem' WHERE scope = 'jit_claim_funds'`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE app_permissions SET scope = 'cash_transfer' WHERE scope = 'jit_transfer'`).Error; err != nil {
			return err
		}

		return nil
	})
}
