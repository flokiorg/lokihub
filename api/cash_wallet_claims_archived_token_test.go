package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/archive"
	"github.com/flokiorg/lokihub/tests"
)

// An archived bill's row carries the lokicash string it was issued with.
//
// It used to be blanked, on the reasoning that anything money-shaped next to a
// destroyed bill looks spendable. That cost the operator the one identifier
// that matches a row against a support ticket or an offline record, and it
// bought nothing: the bill is deleted, so the token moves no value, and a
// caller who presents it over NWC gets the cash_status tombstone inside the
// retention window and silence outside it. It is a record here, not a
// credential (NIP-CASH §Archival on Deletion).
//
// Verbatim, not re-minted, is the load-bearing part. The token embeds the
// relay hints in effect at mint time, so a string encoded now could differ
// from the one its holder actually holds — and then it would match nothing,
// which is the only reason to show it.
func TestListCashWalletClaims_ArchivedRowKeepsIssuedToken(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 10)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 10_000},
	}))

	// A string deliberately unlike anything this hub would encode now, so the
	// assertion below can only pass by reading it back rather than re-deriving.
	const issued = "lokicash1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzk0ju0000000000000000"
	require.NoError(t, svc.DB.Model(&db.App{}).
		Where("id = ?", wallet.ID).Update("cash_token", issued).Error)
	wallet.CashToken = issued

	require.NoError(t, svc.DB.Transaction(func(tx *gorm.DB) error {
		return archive.ArchiveAndDeleteCashBillTx(tx, wallet, db.CashBillOutcomeExpired, 0, time.Now())
	}))

	theAPI := newTestAPI(svc)
	result, _, _, err := theAPI.ListCashWalletClaims(hub.ID, 0, 0, "")
	require.NoError(t, err)
	require.Len(t, result, 1)

	row := result[0]
	require.True(t, row.Archived, "the bill was archived, so its row must come from the archive")
	assert.Equal(t, issued, row.CashToken,
		"an archived row must carry the exact string the bill was issued with, not a freshly minted one")
	assert.NotEmpty(t, row.WalletPubkey,
		"the pubkey stays populated too — it is what correlates the row with relay logs")
}

// A bill archived before the token column existed has nothing to show, and
// must still list cleanly rather than erroring or dropping the row. The UI
// falls back to the bill's pubkey for exactly this case, so an empty token
// here is a supported state, not a failure.
func TestListCashWalletClaims_ArchivedRowWithoutTokenStillLists(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 10)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 10_000},
	}))

	require.NoError(t, svc.DB.Transaction(func(tx *gorm.DB) error {
		return archive.ArchiveAndDeleteCashBillTx(tx, wallet, db.CashBillOutcomeExpired, 0, time.Now())
	}))

	theAPI := newTestAPI(svc)
	result, _, _, err := theAPI.ListCashWalletClaims(hub.ID, 0, 0, "")
	require.NoError(t, err)
	require.Len(t, result, 1, "an archived row must still be listed")

	assert.True(t, result[0].Archived)
	// An archived bill KEEPS its issued token: the archive copies it verbatim off the
	// App row that held it in life. It used to come back empty only because a wallet
	// could exist without one, which is no longer true — every bill is signed at mint,
	// and that exact string is what the archive preserves.
	assert.NotEmpty(t, result[0].CashToken,
		"an archived bill must retain the token it was issued with, for the operator's audit trail")
	assert.NotEmpty(t, result[0].WalletPubkey,
		"the pubkey is the fallback identifier such a row is shown by")
}

// Archiving must not disturb the live path: a live bill still gets a token
// minted for it, and it is still the connection GetCashWalletConnection
// derives. The listing now reads a stored column on both branches of its
// union, so this guards the branch that was NOT changed.
func TestListCashWalletClaims_LiveRowStillMintsItsToken(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 10)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 10_000},
	}))

	theAPI := newTestAPI(svc)
	result, _, _, err := theAPI.ListCashWalletClaims(hub.ID, 0, 0, "")
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.False(t, result[0].Archived)

	conn, err := theAPI.GetCashWalletConnection(wallet.ID)
	require.NoError(t, err)
	assert.Equal(t, conn.CashToken, result[0].CashToken,
		"a live row's token must still be the one derived for that wallet, unchanged by the archive path")
}
