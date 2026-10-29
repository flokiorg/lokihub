package migrations

import (
	"gorm.io/gorm"
)

// EnsurePartialIndexes creates the partial indexes AutoMigrate cannot declare.
// Same syntax on sqlite and postgres; each is a no-op once it exists.
func EnsurePartialIndexes(db *gorm.DB) error {
	// One live slice per identity per wallet. Only unclaimed rows count: a full
	// transfer to an identity that once held a slice on the wallet is in place
	// (NIP-CASH §Which outcome a request produces). The full unique index an
	// earlier build declared on the same columns is dropped first.
	if err := db.Exec(`DROP INDEX IF EXISTS idx_cash_claim_wallet_identity`).Error; err != nil {
		return err
	}
	if err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_cash_claim_wallet_identity_live
		ON cash_wallet_claims (wallet_app_id, identity_type, identity_value)
		WHERE claimed_at IS NULL
	`).Error; err != nil {
		return err
	}

	// Sub-wallet rows awaiting cleanup, so the expiry sweep stays O(log expired).
	if err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_apps_cleanup
		ON apps (expires_at)
		WHERE parent_app_id IS NOT NULL AND cleanup_in_progress = false
	`).Error; err != nil {
		return err
	}

	// At most one pending rotation names a new connection key (NIP-CW
	// §Recovering a Lost Connection).
	if err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_circle_rotations_pending_key
		ON circle_rotations (new_connection_pubkey)
		WHERE state = 'pending'
	`).Error; err != nil {
		return err
	}

	// Every NWC request is routed to an app by its app_pubkey, so two apps must never
	// share one. prepareApp checks before inserting; this index makes a concurrent
	// second insert fail, which saveAppTx maps to ErrDuplicateAppPubkey.
	return db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_apps_app_pubkey_unique
		ON apps (app_pubkey)
		WHERE app_pubkey <> ''
	`).Error
}
