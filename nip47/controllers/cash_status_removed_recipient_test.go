package controllers

import (
	"testing"

	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
)

// NIP-CASH §Removing a Recipient: a removed slice is terminal (reclaimed), is
// hidden from both cash_status scopes, and still counts toward the bill's
// recipient history.
func TestCashStatus_RemovedRecipientHiddenFromBothScopes(t *testing.T) {
	svc, wallet, mine, theirs := scopeFixture(t)

	var removed db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ? AND identity_value = ?", wallet.ID, theirs).First(&removed).Error)
	_, err := svc.AppsService.DeleteCashClaim(wallet.ID, removed.ID, nil)
	require.NoError(t, err)

	all := callStatus(t, svc, wallet, statusRequest(t, nipcash.ScopeAll), &CashStatusCaller{IdentityValue: mine})
	require.Nil(t, all.Error)
	allRows := all.Result.(nipcash.CashStatusResult).Recipients
	require.Len(t, allRows, 1, "scope=all lists only the remaining recipient")
	assert.Equal(t, mine, allRows[0].IdentityValue)

	own := callStatus(t, svc, wallet, statusRequest(t, nipcash.ScopeMine), &CashStatusCaller{IdentityValue: theirs})
	require.Nil(t, own.Error)
	assert.Empty(t, own.Result.(nipcash.CashStatusResult).Recipients,
		"the removed recipient's own scope=mine read returns nothing")

	var archived db.CashBillSliceArchive
	require.NoError(t, svc.DB.Where("wallet_app_id = ? AND identity_value = ?", wallet.ID, theirs).First(&archived).Error)
	assert.Equal(t, db.CashSliceStatusReclaimed, archived.Outcome)
	hadRemoved, err := svc.AppsService.HasArchivedSliceForWallet(wallet.ID)
	require.NoError(t, err)
	assert.True(t, hadRemoved, "the removed recipient still counts toward the bill's recipient history")
}
