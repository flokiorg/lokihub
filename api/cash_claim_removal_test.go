package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Removing a Recipient: the removed slice's amount MUST be swept back
// to the Cash Hub in the same atomic step. When the sweep can't happen (here the
// bill holds no funds), nothing is removed: the slice stays live and redeemable,
// and no "reclaimed" record is written for a slice that was never reclaimed.
func TestDeleteCashClaim_SweepFails_SliceUntouched(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 300_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 200)
	removed, kept := tests.RandomHex32(), tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: removed, AmountMloki: 100_000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: kept, AmountMloki: 100_000},
	}))
	// Deliberately unfunded, so the sweep back to the hub fails.

	var claim db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ? AND identity_value = ?", wallet.ID, removed).First(&claim).Error)

	err = newTestAPIWithService(t, svc).DeleteCashClaim(hub.ID, wallet.ID, claim.ID)
	require.Error(t, err, "a removal whose sweep fails must fail")

	var live db.CashWalletClaim
	require.NoError(t, svc.DB.First(&live, claim.ID).Error, "the slice is still there")
	assert.Nil(t, live.ClaimedAt, "and still unclaimed, so its recipient can redeem it")

	archived, err := svc.AppsService.HasArchivedSliceForWallet(wallet.ID)
	require.NoError(t, err)
	assert.False(t, archived, "nothing was reclaimed, so nothing is archived as reclaimed")
}
