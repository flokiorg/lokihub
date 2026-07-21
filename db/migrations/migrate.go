package migrations

import (
	"github.com/flokiorg/lokihub/db"
	"gorm.io/gorm"
)

func Migrate(gormDB *gorm.DB) error {
	// Run manual migrations first (for schema changes AutoMigrate can't handle)
	if err := MigrateTransactionsFK(gormDB); err != nil {
		return err
	}

	if err := MigrateTransactionsRequestEventFK(gormDB); err != nil {
		return err
	}

	// Backfill kind column from isolated bool, then drop isolated.
	if err := MigrateAppKind(gormDB); err != nil {
		return err
	}

	// Create jit_hub_configs + circle_hub_configs, rename kinds, backfill configs.
	if err := MigrateJITCircleConfigTables(gormDB); err != nil {
		return err
	}

	// Rename stored kind value "circle_child" → "circle_wallet".
	if err := MigrateRenameCircleChildToCircleWallet(gormDB); err != nil {
		return err
	}

	// Extract Circle policy/pubkey/allowlist into a standalone, reusable
	// CircleIdentity that survives deletion of any single circle_hub.
	if err := MigrateCircleIdentityTables(gormDB); err != nil {
		return err
	}

	// Rename stored config key "CircleRelay" → "GeneralRelay".
	if err := MigrateRenameCircleRelayToGeneralRelay(gormDB); err != nil {
		return err
	}

	// Add + backfill circle_identity_allowed_pubkeys.created_at, so allowlist
	// policies can report "last policy update" alongside following policies.
	if err := MigrateCircleIdentityAllowlistTimestamps(gormDB); err != nil {
		return err
	}

	// Swap relay.nostr.band for nos.lol in an existing GeneralRelay config.
	if err := MigrateReplaceNostrBandGeneralRelay(gormDB); err != nil {
		return err
	}

	// AutoMigrate all core models (adds new columns declared in structs)
	// Note: LSP model is migrated separately in LSPManager (via manager_db.go)
	if err := gormDB.AutoMigrate(
		&db.UserConfig{},
		&db.App{},
		&db.AppPermission{},
		&db.RequestEvent{},
		&db.ResponseEvent{},
		&db.Transaction{},
		&db.Swap{},
		&db.Forward{},
		&db.CircleIdentity{},
		&db.CircleIdentityAllowedPubkey{},
		&db.JITHubConfig{},
		&db.CircleHubConfig{},
		&db.JITWalletClaim{},
		&db.CircleWalletIdentityProof{},
		&db.CircleWalletMembership{},
	); err != nil {
		return err
	}

	// Partial index on apps(expires_at) for sub-wallets awaiting cleanup.
	// Runs after AutoMigrate since it indexes parent_app_id/cleanup_in_progress, which AutoMigrate adds.
	return MigrateCleanupIndex(gormDB)
}
