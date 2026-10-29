package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// An ordered bill is ended only by its holder (NIP-CASH §Data Model): the
// owner's generic app delete (Disconnect, DELETE /api/apps/:pubkey) refuses it
// as the Cash Hub's wallet delete does, and sweeps nothing to the Hub.
func TestDeleteApp_OrderedBill_Refused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	bill, _, err := svc.AppsService.CreateApp(
		"ordered-bill", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.GET_BALANCE_SCOPE}, db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(bill.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 5_000, Ordered: true},
	}))

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteApp(bill)
	require.ErrorIs(t, err, constants.ErrInvalidParams)
	assert.Contains(t, err.Error(), "ordered slice")
	err = theAPI.DeleteCashWallet(hub.ID, bill.ID)
	require.ErrorIs(t, err, constants.ErrInvalidParams)

	var n int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", bill.ID).Count(&n).Error)
	assert.EqualValues(t, 1, n, "the ordered bill stands")

	// Once its holder has ended the slice, the bill is the owner's to delete.
	require.NoError(t, svc.DB.Model(&db.CashWalletClaim{}).Where("wallet_app_id = ?", bill.ID).
		Update("claimed_at", time.Now()).Error)
	require.NoError(t, theAPI.DeleteApp(bill))
}

// A bill a cancelled withdrawal re-minted is the holder's too (NIP-CASH
// §Cancelling a Payout): neither delete path nor the slice removal takes it.
func TestDeleteApp_RemintedBill_Refused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	bill, _, err := svc.AppsService.CreateApp(
		"reminted-bill", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.GET_BALANCE_SCOPE}, db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(bill.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 5_000, Reminted: true},
	}))
	var claim db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", bill.ID).First(&claim).Error)

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteApp(bill)
	require.ErrorIs(t, err, constants.ErrInvalidParams)
	assert.Contains(t, err.Error(), "re-minted")
	require.ErrorIs(t, theAPI.DeleteCashWallet(hub.ID, bill.ID), constants.ErrInvalidParams)
	require.ErrorIs(t, theAPI.DeleteCashClaim(hub.ID, bill.ID, claim.ID), constants.ErrInvalidParams)
}

// NIP-CASH §Removing a Recipient (CASH-455): the owner never removes an ordered
// slice; a slice beside it that is not ordered is removed.
func TestDeleteCashClaim_OrderedSliceRefused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	bill, _, err := svc.AppsService.CreateApp(
		"ordered-bill", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE}, db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(bill.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 5_000, Ordered: true},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 5_000},
	}))
	tests.FundApp(svc, bill.ID, 10_000, "ordered-bill-fund")
	var ordered, plain db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ? AND ordered = ?", bill.ID, true).First(&ordered).Error)
	require.NoError(t, svc.DB.Where("wallet_app_id = ? AND ordered = ?", bill.ID, false).First(&plain).Error)

	theAPI := newTestAPIWithService(t, svc)
	require.ErrorIs(t, theAPI.DeleteCashClaim(hub.ID, bill.ID, ordered.ID), constants.ErrInvalidParams)
	var n int64
	require.NoError(t, svc.DB.Model(&db.CashWalletClaim{}).Where("id = ? AND claimed_at IS NULL", ordered.ID).Count(&n).Error)
	require.EqualValues(t, 1, n, "the ordered slice stands, unclaimed")

	require.NoError(t, theAPI.DeleteCashClaim(hub.ID, bill.ID, plain.ID), "the slice beside it is removed")
}
