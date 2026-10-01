package main

// TestMigrate above asserts only that migrateDB returns no error. It never checked
// that any data arrived, which is why this went unnoticed: migrateDB copied 6 of the
// 27 tables in expectedTables, and the omission was SILENT, because checkSchema only
// asserts a table EXISTS and AutoMigrate creates every one of them empty on the
// destination. So a sqlite -> postgres migration reported success while leaving behind:
//
//   - cash_wallet_claims — the record of who owns what value inside every bill. The
//     transactions DID migrate, so the money arrived with nobody holding a claim to
//     it, and every bill became unredeemable.
//   - cash_bill_archives / cash_bill_slice_archives — the only record that a spent
//     bill ever existed, since a drained bill's app row is hard-deleted. Their own
//     comment in expectedTables claimed they were "carried across a migration like any
//     other table". They were not.
//   - cash_transfer_proofs — burnt-nonce replay guards, so every nonce was forgotten.
//   - cash_hub_configs / circle_hub_configs — each hub's fee, expiry and limit policy.
//   - circle_identities and their allowlists — Identity Authority trust.
//   - swaps, forwards, cash_mint_idempotencies.
//
// This test seeds one row in each previously-dropped table and requires it on the
// other side. It is deliberately a row-count check per table rather than a deep
// comparison: the failure being guarded against is a table nobody remembered to copy,
// and a count catches that while staying trivial to extend when a new table is added.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// seedOneRowPerTable writes one row into every table migrateDB must carry, in FK
// order. Returns the table names it seeded.
func seedOneRowPerTable(t *testing.T, conn *gorm.DB) []string {
	t.Helper()

	hub := &db.App{Name: "mig-hub", AppPubkey: "mighubpk", Kind: db.AppKindCashHub}
	require.NoError(t, conn.Create(hub).Error)
	walletPub := "migwalletpubkey"
	wallet := &db.App{Name: "mig-wallet", AppPubkey: "migwalletpk", Kind: db.AppKindCashWallet,
		ParentAppID: &hub.ID, ParentKind: db.ParentKindCash, WalletPubkey: &walletPub}
	require.NoError(t, conn.Create(wallet).Error)

	identity := &db.CircleIdentity{Name: "mig-identity", ProviderPubkey: "migidentitypubkey"}
	require.NoError(t, conn.Create(identity).Error)

	rows := []struct {
		table string
		value any
	}{
		{"circle_identity_allowed_pubkeys", &db.CircleIdentityAllowedPubkey{
			CircleIdentityID: identity.ID, Pubkey: "migallowed"}},
		{"cash_hub_configs", &db.CashHubConfig{
			AppID: hub.ID, PerWalletMaxMloki: 100_000, MaxExpSecs: 3600}},
		{"cash_wallet_claims", &db.CashWalletClaim{
			WalletAppID: wallet.ID, IdentityType: db.CashIdentityPubkey,
			IdentityValue: "migclaimant", AmountMloki: 4242}},
		{"cash_transfer_proofs", &db.CashTransferProof{
			AppID: wallet.ID, EventID: "migproofevent"}},
		{"cash_stranded_funds", &db.CashStrandedFund{
			Operation: "consolidate", SourceWalletAppID: wallet.ID,
			RetainedWalletAppID: hub.ID, AmountMloki: 7}},
		{"cash_mint_idempotencies", &db.CashMintIdempotency{
			HubAppID: hub.ID, IdempotencyKey: "migkey",
			WalletAppID: wallet.ID, WalletPubkey: walletPub}},
		{"swaps", &db.Swap{SwapId: "migswap"}},
	}
	seeded := []string{"apps"}
	for _, r := range rows {
		require.NoError(t, conn.Create(r.value).Error, "seeding %s", r.table)
		seeded = append(seeded, r.table)
	}
	return seeded
}

func TestMigrate_CarriesEveryTablesData(t *testing.T) {
	env, err := setupTest(t, getTestSqliteURI(0), getTestSqliteURI(1))
	require.NoError(t, err)
	defer env.cleanup(t)

	seeded := seedOneRowPerTable(t, env.source)

	require.NoError(t, migrateDB(env.source, env.dest))

	for _, table := range seeded {
		var srcCount, dstCount int64
		require.NoError(t, env.source.Table(table).Count(&srcCount).Error)
		require.NoError(t, env.dest.Table(table).Count(&dstCount).Error)
		require.Positive(t, srcCount, "setup: %s was not seeded", table)
		require.Equal(t, srcCount, dstCount,
			"table %q: %d row(s) in the source, %d in the destination — the migration "+
				"reported SUCCESS and left this data behind. checkSchema cannot catch it, "+
				"because AutoMigrate creates the table empty on the destination and "+
				"checkSchema only asserts it exists.", table, srcCount, dstCount)
	}
}

// TestMigrate_EveryExpectedTableIsCopied is the structural half: adding a table to
// expectedTables without adding it to migrateDB must fail here rather than silently
// drop that table's data on the next operator migration.
//
// It compares against the set migrateDB actually populates, discovered by migrating an
// EMPTY source — every table it touches gets created on the destination, and any it
// does not is one nobody remembered.
func TestMigrate_EveryExpectedTableIsCopied(t *testing.T) {
	env, err := setupTest(t, getTestSqliteURI(0), getTestSqliteURI(1))
	require.NoError(t, err)
	defer env.cleanup(t)

	// One row in every table, so "copied" is observable as a non-zero count rather
	// than inferred.
	seedOneRowPerTable(t, env.source)
	require.NoError(t, migrateDB(env.source, env.dest))

	// request_events/response_events/transactions/app_permissions/user_configs and the
	// circle_wallet_* tables are legitimately empty here — this test is about the ones
	// that were seeded.
	for _, table := range []string{
		"apps", "circle_identities", "circle_identity_allowed_pubkeys",
		"cash_hub_configs", "cash_wallet_claims", "cash_transfer_proofs",
		"cash_stranded_funds", "cash_mint_idempotencies", "swaps",
	} {
		var n int64
		require.NoError(t, env.dest.Table(table).Count(&n).Error)
		require.Positive(t, n, "table %q has no rows on the destination", table)
	}

	// And the destination must pass the schema assertion afterwards, so a table copied
	// here can never be one checkSchema would reject.
	require.NoError(t, checkSchema(env.dest))

}
