package migrations

import (
	"strings"

	"gorm.io/gorm"
)

// MigrateTransactionsFK adds ON DELETE CASCADE to the transactions.app_id FK.
// This is required because GORM's AutoMigrate does not update existing FK constraints in SQLite.
// The standard SQLite approach is to recreate the table with the correct schema.
func MigrateTransactionsFK(db *gorm.DB) error {
	// Check if the table exists. If not (fresh DB), AutoMigrate will create it correctly.
	if !db.Migrator().HasTable("transactions") {
		return nil
	}

	// Check if we need to migrate by looking at the current schema
	var tableSql string
	err := db.Raw("SELECT sql FROM sqlite_master WHERE type='table' AND name='transactions'").Scan(&tableSql).Error
	if err != nil {
		return err
	}

	// If the constraint already has ON DELETE CASCADE, skip migration
	if strings.Contains(tableSql, "ON DELETE CASCADE") {
		return nil // Already migrated
	}

	// SQLite requires table recreation to change FK constraints
	// 1. Create new table with correct schema
	// 2. Copy data
	// 3. Drop old table
	// 4. Rename new table

	return db.Transaction(func(tx *gorm.DB) error {
		// Get column list dynamically to avoid issues if columns change
		columns := []string{
			"id", "app_id", "request_event_id", "type", "state",
			"amount_mloki", "fee_mloki", "fee_reserve_mloki",
			"payment_request", "payment_hash", "description", "description_hash",
			"preimage", "created_at", "expires_at", "updated_at", "settled_at",
			"metadata", "self_payment", "boostagram", "failure_reason", "hold", "settle_deadline",
		}
		columnList := strings.Join(columns, ", ")

		// Create new table with correct FK constraint
		createSQL := `
			CREATE TABLE transactions_new (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				app_id INTEGER,
				request_event_id INTEGER,
				type TEXT,
				state TEXT,
				amount_mloki INTEGER,
				fee_mloki INTEGER,
				fee_reserve_mloki INTEGER,
				payment_request TEXT,
				payment_hash TEXT,
				description TEXT,
				description_hash TEXT,
				preimage TEXT,
				created_at DATETIME,
				expires_at DATETIME,
				updated_at DATETIME,
				settled_at DATETIME,
				metadata JSON,
				self_payment NUMERIC,
				boostagram JSON,
				failure_reason TEXT,
				hold NUMERIC,
				settle_deadline INTEGER,
				CONSTRAINT fk_transactions_request_event FOREIGN KEY (request_event_id) REFERENCES request_events(id),
				CONSTRAINT fk_transactions_app FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE
			)
		`
		if err := tx.Exec(createSQL).Error; err != nil {
			return err
		}

		// Copy data
		copySQL := "INSERT INTO transactions_new (" + columnList + ") SELECT " + columnList + " FROM transactions"
		if err := tx.Exec(copySQL).Error; err != nil {
			return err
		}

		// Drop old table
		if err := tx.Exec("DROP TABLE transactions").Error; err != nil {
			return err
		}

		// Rename new table
		if err := tx.Exec("ALTER TABLE transactions_new RENAME TO transactions").Error; err != nil {
			return err
		}

		return nil
	})
}
