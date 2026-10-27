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
