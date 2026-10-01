package main

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

var expectedTables = []string{
	"apps",
	"app_permissions",
	"circle_identities",
	"circle_identity_allowed_pubkeys",
	"circle_hub_configs",
	"circle_wallet_identity_proofs",
	"circle_wallet_memberships",
	"cash_wallet_claims",
	"cash_hub_configs",
	"request_events",
	"response_events",
	"transactions",
	"swaps",
	"user_configs",
	"forwards",
	"circle_identities",
	"circle_identity_allowed_pubkeys",
	"cash_hub_configs",
	"circle_hub_configs",
	"cash_wallet_claims",
	"circle_wallet_identity_proofs",
	"circle_wallet_memberships",
	"cash_transfer_proofs",
	"cash_stranded_funds",
	// The cash bill archive. Retained forever and carried across a migration
	// like any other table: it is the only record that a spent bill ever
	// existed, since the bill's own app row is hard-deleted the moment it is
	// drained.
	"cash_bill_archives",
	"cash_bill_slice_archives",
	// mint_cash's replay guard — losing it forgets every served key, so a caller's
	// retry could mint a second wallet on the new database.
	"cash_mint_idempotencies",
}

func main() {
	var fromDSN, toDSN string

	logger.Init(strconv.Itoa(int(4)))

	flag.StringVar(&fromDSN, "from", "", "source DSN")
	flag.StringVar(&toDSN, "to", "", "destination DSN")

	flag.Parse()

	if fromDSN == "" || toDSN == "" {
		flag.Usage()
		logger.Logger.Error().Msg("missing DSN")
		os.Exit(1)
	}

	stopDB := func(d *gorm.DB) {
		if err := db.Stop(d); err != nil {
			logger.Logger.Error().Err(err).Msg("failed to close database")
		}
	}

	logger.Logger.Info().Msg("opening source DB...")
	fromDB, err := db.NewDB(fromDSN, false)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to open source database")
		os.Exit(1)
	}
	defer stopDB(fromDB)

	logger.Logger.Info().Msg("opening destination DB...")
	toDB, err := db.NewDB(toDSN, false)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to open destination database")
		os.Exit(1)
	}
	defer stopDB(toDB)

	// Migrations are applied to both the source and the target DB, so
	// schemas should be equal at this point.
	err = checkSchema(fromDB)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("database schema check failed; the migration tool may be outdated")
		os.Exit(1)
	}

	// Check if VSS is enabled in the source database
	var vssConfig db.UserConfig
	result := fromDB.Where("key = ?", "LdkVssEnabled").First(&vssConfig)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			logger.Logger.Error().Msg("LdkVssEnabled config not found in source DB. Migration will not proceed.")
		} else {
			logger.Logger.Error().Err(result.Error).Msg("failed to query LdkVssEnabled config from source DB")
		}
		os.Exit(1)
	}

	if vssConfig.Value != "true" {
		logger.Logger.Error().Msg("VSS is not enabled in the source DB (LdkVssEnabled is not 'true'). Migration will not proceed.")
		os.Exit(1)
	}
	logger.Logger.Info().Msg("LdkVssEnabled check passed.")

	// NOTE: we assume that excess request events have already been cleaned up due to the background task
	// and only a maximum of ~1000 remain.
	logger.Logger.Info().Msg("Deleting orphaned request events.")
	err = fromDB.Exec("DELETE FROM request_events WHERE app_id NOT IN (SELECT id FROM apps);").Error

	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to delete orphaned request events")
		os.Exit(1)
	}

	// NOTE: we assume that excess response events have already been cleaned up due to the background task
	// and only a maximum of ~1000 remain.
	logger.Logger.Info().Msg("Deleting orphaned response events.")
	err = fromDB.Exec("DELETE FROM response_events WHERE request_id NOT IN (SELECT id FROM request_events);").Error

	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to delete orphaned response events")
		os.Exit(1)
	}

	logger.Logger.Info().Msg("migrating...")
	err = migrateDB(fromDB, toDB)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to migrate database")
		os.Exit(1)
	}

	logger.Logger.Info().Msg("migration complete")
}

func migrateDB(from, to *gorm.DB) error {
	tx := to.Begin()
	defer tx.Rollback()

	if err := tx.Error; err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}

	// Table migration order matters: referenced tables must be migrated
	// before referencing tables.

	logger.Logger.Info().Msg("migrating apps...")
	if err := migrateTable[db.App](from, tx); err != nil {
		return fmt.Errorf("failed to migrate apps: %w", err)
	}

	logger.Logger.Info().Msg("migrating app_permissions...")
	if err := migrateTable[db.AppPermission](from, tx); err != nil {
		return fmt.Errorf("failed to migrate app_permissions: %w", err)
	}

	logger.Logger.Info().Msg("migrating request_events...")
	if err := migrateTable[db.RequestEvent](from, tx); err != nil {
		return fmt.Errorf("failed to migrate request_events: %w", err)
	}

	logger.Logger.Info().Msg("migrating response_events...")
	if err := migrateTable[db.ResponseEvent](from, tx); err != nil {
		return fmt.Errorf("failed to migrate response_events: %w", err)
	}

	logger.Logger.Info().Msg("migrating transactions...")
	if err := migrateTable[db.Transaction](from, tx); err != nil {
		return fmt.Errorf("failed to migrate transactions: %w", err)
	}

	logger.Logger.Info().Msg("migrating user_configs...")
	if err := migrateTable[db.UserConfig](from, tx); err != nil {
		return fmt.Errorf("failed to migrate user_configs: %w", err)
	}

	// Everything below was NOT migrated, and the omission was silent: checkSchema
	// only asserts that a table EXISTS, and AutoMigrate creates each of these empty
	// on the destination, so the migration reported success while leaving all of them
	// behind. 6 of 27 tables were copied.
	//
	// What that cost, concretely: cash_wallet_claims is the record of who owns what
	// value inside every bill, so the transactions carried over above would have
	// landed with nobody holding a claim to them — every bill unredeemable. The bill
	// archives are the ONLY record that a spent bill ever existed (its app row is
	// hard-deleted on drain), and their own comment in expectedTables claimed they
	// were "carried across a migration like any other table", which was not true.
	// cash_transfer_proofs are burnt-nonce replay guards, so losing them forgets every
	// nonce. The hub configs are each hub's fee, expiry and limit policy.
	//
	// Order matters for the same reason it does above: referenced tables first.
	// circle_identities is standalone and must precede both its allowlist and
	// circle_hub_configs, which reference it.

	logger.Logger.Info().Msg("migrating circle_identities...")
	if err := migrateTable[db.CircleIdentity](from, tx); err != nil {
		return fmt.Errorf("failed to migrate circle_identities: %w", err)
	}

	logger.Logger.Info().Msg("migrating circle_identity_allowed_pubkeys...")
	if err := migrateTable[db.CircleIdentityAllowedPubkey](from, tx); err != nil {
		return fmt.Errorf("failed to migrate circle_identity_allowed_pubkeys: %w", err)
	}

	logger.Logger.Info().Msg("migrating cash_hub_configs...")
	if err := migrateTable[db.CashHubConfig](from, tx); err != nil {
		return fmt.Errorf("failed to migrate cash_hub_configs: %w", err)
	}

	logger.Logger.Info().Msg("migrating circle_hub_configs...")
	if err := migrateTable[db.CircleHubConfig](from, tx); err != nil {
		return fmt.Errorf("failed to migrate circle_hub_configs: %w", err)
	}

	logger.Logger.Info().Msg("migrating cash_wallet_claims...")
	if err := migrateTable[db.CashWalletClaim](from, tx); err != nil {
		return fmt.Errorf("failed to migrate cash_wallet_claims: %w", err)
	}

	logger.Logger.Info().Msg("migrating circle_wallet_memberships...")
	if err := migrateTable[db.CircleWalletMembership](from, tx); err != nil {
		return fmt.Errorf("failed to migrate circle_wallet_memberships: %w", err)
	}

	logger.Logger.Info().Msg("migrating circle_wallet_identity_proofs...")
	if err := migrateTable[db.CircleWalletIdentityProof](from, tx); err != nil {
		return fmt.Errorf("failed to migrate circle_wallet_identity_proofs: %w", err)
	}

	logger.Logger.Info().Msg("migrating cash_transfer_proofs...")
	if err := migrateTable[db.CashTransferProof](from, tx); err != nil {
		return fmt.Errorf("failed to migrate cash_transfer_proofs: %w", err)
	}

	logger.Logger.Info().Msg("migrating cash_stranded_funds...")
	if err := migrateTable[db.CashStrandedFund](from, tx); err != nil {
		return fmt.Errorf("failed to migrate cash_stranded_funds: %w", err)
	}

	logger.Logger.Info().Msg("migrating cash_bill_archives...")
	if err := migrateTable[db.CashBillArchive](from, tx); err != nil {
		return fmt.Errorf("failed to migrate cash_bill_archives: %w", err)
	}

	logger.Logger.Info().Msg("migrating cash_bill_slice_archives...")
	if err := migrateTable[db.CashBillSliceArchive](from, tx); err != nil {
		return fmt.Errorf("failed to migrate cash_bill_slice_archives: %w", err)
	}

	logger.Logger.Info().Msg("migrating cash_mint_idempotencies...")
	if err := migrateTable[db.CashMintIdempotency](from, tx); err != nil {
		return fmt.Errorf("failed to migrate cash_mint_idempotencies: %w", err)
	}

	logger.Logger.Info().Msg("migrating swaps...")
	if err := migrateTable[db.Swap](from, tx); err != nil {
		return fmt.Errorf("failed to migrate swaps: %w", err)
	}

	logger.Logger.Info().Msg("migrating forwards...")
	if err := migrateTable[db.Forward](from, tx); err != nil {
		return fmt.Errorf("failed to migrate forwards: %w", err)
	}

	if to.Name() == "postgres" {
		logger.Logger.Info().Msg("resetting sequences...")
		if err := resetSequences(tx); err != nil {
			return fmt.Errorf("failed to reset sequences: %w", err)
		}
	}

	tx.Commit()
	if err := tx.Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func migrateTable[T any](from, to *gorm.DB) error {
	var data []T
	if err := from.Find(&data).Error; err != nil {
		return fmt.Errorf("failed to fetch data: %w", err)
	}

	if len(data) == 0 {
		return nil
	}

	// to avoid "failed to migrate transactions: failed to insert data: extended protocol limited to 65535 parameters"
	// see https://stackoverflow.com/questions/77372430/extended-protocol-limited-to-65535-parameters-golang-gorm
	// max statements is 65535
	// but it's the number of records * columns
	// to be safe, using a lower value of 1000.
	// this will fail if any table has more than 65 columns, which I doubt we will have
	max := 1000
	for i := 0; i < len(data); i += max {
		j := min(i+max, len(data))

		if err := to.Create(data[i:j]).Error; err != nil {
			return fmt.Errorf("failed to insert data: %w", err)
		}
	}

	return nil
}

func checkSchema(db *gorm.DB) error {
	tables, err := listTables(db)
	if err != nil {
		return fmt.Errorf("failed to list database tables: %w", err)
	}

	for _, table := range expectedTables {
		if !slices.Contains(tables, table) {
			return fmt.Errorf("table missing from the database: %q", table)
		}
	}

	for _, table := range tables {
		if !slices.Contains(expectedTables, table) {
			return fmt.Errorf("unexpected table found in the database: %q", table)
		}
	}

	return nil
}

func listTables(db *gorm.DB) ([]string, error) {
	var query string

	switch db.Name() {
	case "sqlite":
		query = "SELECT name FROM sqlite_master WHERE type='table'  AND name NOT LIKE 'sqlite_%';"
	case "postgres":
		query = "SELECT tablename FROM pg_tables WHERE schemaname = 'public';"
	default:
		return nil, fmt.Errorf("unsupported database: %q", db.Name())
	}

	rows, err := db.Raw(query).Rows()
	if err != nil {
		return nil, fmt.Errorf("failed to query table names: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			logger.Logger.Error().Err(err).Msg("failed to close rows")
		}
	}()

	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("failed to scan table name: %w", err)
		}
		tables = append(tables, table)
	}

	return tables, nil
}

func resetSequences(db *gorm.DB) error {
	// Driven by expectedTables, not a second hand-maintained list. It used to name the
	// same six tables migrateDB copied, so when migrateDB grew to cover all 27 this
	// would have left 21 sequences at their start value while rows with higher ids
	// existed — the next insert on the migrated database would collide on the primary
	// key. One list, so the two cannot drift apart again.
	for _, table := range expectedTables {
		if err := resetPostgresSequence(db, table); err != nil {
			return fmt.Errorf("failed to reset the id sequence for %q: %w", table, err)
		}
	}

	return nil
}

// resetPostgresSequence sets table's id sequence to its current maximum id.
//
// It ASKS postgres for the sequence name via pg_get_serial_sequence rather than
// deriving it. The previous version hardcoded names, and two of them were
// `apps_2_id_seq` / `app_permissions_2_id_seq` rather than the `<table>_id_seq` the
// others followed — a leftover of a table rename. So any naming rule inferred from
// that list would have been wrong for exactly the tables where it mattered, and
// guessing wrong makes setval error and fails the whole migration.
//
// A table with no serial id, or no rows, is skipped: setval(seq, NULL) is an error,
// and an empty table's sequence is already correct at its start value.
func resetPostgresSequence(db *gorm.DB, table string) error {
	var seq *string
	if err := db.Raw("SELECT pg_get_serial_sequence(?, 'id')", table).Scan(&seq).Error; err != nil {
		return fmt.Errorf("failed to look up the sequence: %w", err)
	}
	if seq == nil || *seq == "" {
		return nil
	}

	var maxID *int64
	// table comes from expectedTables, a constant in this file — never from input.
	if err := db.Raw(fmt.Sprintf("SELECT MAX(id) FROM %s", table)).Scan(&maxID).Error; err != nil { //nolint:gosec // table is from this file's own constant list
		return fmt.Errorf("failed to read the maximum id: %w", err)
	}
	if maxID == nil {
		return nil
	}

	if err := db.Exec("SELECT setval(?, ?)", *seq, *maxID).Error; err != nil {
		return fmt.Errorf("failed to execute setval(): %w", err)
	}

	return nil
}
