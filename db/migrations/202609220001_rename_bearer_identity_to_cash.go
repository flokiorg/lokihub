package migrations

import (
	"gorm.io/gorm"
)

// MigrateRenameBearerIdentityToCash rewrites stored cash_wallet_claims
// .identity_type values from the old "bearer" spelling to "cash", following
// NIP-CASH's rename of bearer mode to cash mode (the wire now carries
// identity_type: "cash" and cash_secret; see db.CashIdentityCash). Without
// this, every slice minted before the rename keeps an identity_type no
// current code path ever looks up again — its holder's cash_redeem would
// find nothing, so the slice would silently stop being redeemable.
//
// identity_value is deliberately untouched: for a cash-mode slice it is the
// sha256 commitment of the holder's secret, which the rename doesn't change.
// Holders keep redeeming with the secret they already have.
//
// Unlike MigrateRenameJITToCash, this needs no "already done" fast path: the
// WHERE clause makes a second run match zero rows. It also can't collide
// with the unique index (wallet_app_id, identity_type, identity_value) —
// no pre-rename code path ever wrote "cash", so no row can already occupy
// the name this one moves to.
func MigrateRenameBearerIdentityToCash(db *gorm.DB) error {
	if !db.Migrator().HasTable("cash_wallet_claims") {
		return nil // fresh DB; AutoMigrate creates the table and only "cash" is ever written into it
	}

	return db.Transaction(func(tx *gorm.DB) error {
		return tx.Exec(
			`UPDATE cash_wallet_claims SET identity_type = ? WHERE identity_type = ?`,
			"cash", "bearer",
		).Error
	})
}
