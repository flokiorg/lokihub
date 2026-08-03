package migrations

import (
	"gorm.io/gorm"
)

// MigrateDropCashMaxTransfers removes the max_transfers cap feature, dropped
// from NIP-CASH entirely: the hub-level setting on cash_hub_configs, and the
// per-slice inherited value on cash_wallet_claims. cash_wallet_claims'
// transfer_count column is left untouched — it remains in use as the
// internal optimistic-concurrency counter cash_transfer's atomic guards pin
// against (see AppsService.ReassignCashSliceIdentity/SplitCashSliceAmount),
// which has nothing to do with the removed cap.
func MigrateDropCashMaxTransfers(db *gorm.DB) error {
	if !db.Migrator().HasTable("apps") {
		return nil // fresh DB; the current model structs never declare this column
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if tx.Migrator().HasColumn("cash_hub_configs", "max_transfers") {
			if err := tx.Exec(`ALTER TABLE cash_hub_configs DROP COLUMN max_transfers`).Error; err != nil {
				return err
			}
		}
		if tx.Migrator().HasColumn("cash_wallet_claims", "max_transfers") {
			if err := tx.Exec(`ALTER TABLE cash_wallet_claims DROP COLUMN max_transfers`).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
