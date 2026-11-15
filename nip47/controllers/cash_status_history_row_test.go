package controllers

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// R2-F1: after a slice is transferred in place to an identity that once held
// (and redeemed) a slice of the same bill, that identity has a claimed history
// row and a live row. "mine" is exactly its live row; "all" lists every slice,
// the history row too, with the live row first, so a client's OwnRow reads it.
func TestCashStatus_InPlaceTransferToFormerHolder(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 3000)

	xPriv, yPriv := nostr.GeneratePrivateKey(), nostr.GeneratePrivateKey()
	x, _ := nostr.GetPublicKey(xPriv)
	y, _ := nostr.GetPublicKey(yPriv)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: y, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: x, AmountMloki: 2000},
	}))
	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, y)
	require.NoError(t, err)
	_, err = svc.AppsService.ReassignCashSliceIdentity(wallet.ID, db.CashIdentityPubkey, x, db.CashIdentityPubkey, y, "")
	require.NoError(t, err)

	for _, scope := range []string{"", nipcash.ScopeMine, nipcash.ScopeAll} {
		response := callStatus(t, svc, wallet, statusRequest(t, scope), &CashStatusCaller{IdentityValue: y})
		require.Nil(t, response.Error, "scope %q", scope)
		result := response.Result.(nipcash.CashStatusResult)
		if scope == nipcash.ScopeAll {
			// Every slice, the claimed history row included, the live row first.
			require.Len(t, result.Recipients, 2, "scope all: every slice ever recorded")
			assert.False(t, result.Recipients[0].Claimed)
			assert.True(t, result.Recipients[1].Claimed)
			assert.Equal(t, uint64(1000), result.Recipients[1].AmountMillis)
		} else {
			require.Len(t, result.Recipients, 1, "scope %q: one row for the identity", scope)
		}
		row, err := nipcash.OwnRow(result.Recipients, nipcash.BySigning(yPriv), scope)
		require.NoError(t, err, "scope %q", scope)
		require.False(t, row.Claimed, "scope %q: the live row", scope)
		require.Equal(t, uint64(2000), row.AmountMillis)
	}
}
