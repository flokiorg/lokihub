package api

// Covers DeleteJITWallet: unlike DeleteJITWalletClaim (which only ever
// removes a single still-unclaimed slice — see jit_wallet_claims_test.go),
// this removes a real jit_wallet child (and every slice it serves) in any
// state, reclaiming any remaining balance back to the hub before deleting it
// (service.ReclaimAndDeleteSubWallet) — the same pattern
// DeleteCircleWalletChild uses (circle_child_delete_test.go).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
)

func TestDeleteJITWallet_Empty_DeletesWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 10_000, 3600)
	childApp, _, err := svc.AppsService.CreateApp(
		"jit-child", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE}, db.AppKindJITWallet, &hub.ID, db.ParentKindJIT, nil,
	)
	require.NoError(t, err)

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteJITWallet(hub.ID, childApp.ID)
	require.NoError(t, err)

	var count int64
	svc.DB.Model(&db.App{}).Where("id = ?", childApp.ID).Count(&count)
	assert.Zero(t, count, "the empty JIT wallet must be deleted")
}

func TestDeleteJITWallet_ClaimedWithBalance_ReclaimsToHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Recognised as a self-payment so the reclaim transfer's incoming leg on
	// the hub settles synchronously.
	svc.LNClient.(*tests.MockLn).Pubkey = selfPaymentPubkey

	hub := tests.CreateJITHub(t, svc, 300_000, 3600)
	pk := tests.RandomHex32()

	// MockInvoice (used for the reclaim transfer) encodes a fixed 123_000 mloki
	// amount that the mock LN client validates against regardless of the
	// requested amount — fund well above that so the reclaim payment validates.
	const fundedMloki = 200_000

	// A real wallet with a claimed slice, funded directly (tests.FundApp seeds
	// a settled balance without going through MakeInvoice/SendPaymentSync,
	// whose mock invoice is fixed per test and would otherwise collide with
	// the reclaim transfer below).
	childApp, _, err := svc.AppsService.CreateApp(
		"jit-child", "", fundedMloki/1000, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE}, db.AppKindJITWallet, &hub.ID, db.ParentKindJIT, nil,
	)
	require.NoError(t, err)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(childApp.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk, AmountMloki: fundedMloki},
	}))
	_, err = svc.AppsService.ClaimJITWalletSlice(childApp.ID, db.JITAllocIdentityPubkey, pk)
	require.NoError(t, err)
	tests.FundApp(svc, childApp.ID, fundedMloki, "fundtxhash")

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteJITWallet(hub.ID, childApp.ID)
	require.NoError(t, err)

	var appCount int64
	svc.DB.Model(&db.App{}).Where("id = ?", childApp.ID).Count(&appCount)
	assert.Zero(t, appCount, "the wallet app must be deleted")

	// The wallet's balance must be transferred back to the hub, not destroyed.
	// (The mock LN client always settles MakeInvoice at a fixed canned amount
	// regardless of what's requested, so this only checks direction/sign, the
	// same way TestRunJITCleanup_WithBalance_TransferAndDeleted does.)
	hubBalance := queries.GetIsolatedBalance(svc.DB, hub.ID)
	assert.Greater(t, hubBalance, int64(0), "hub must gain balance after the reclaim transfer")
}

// TestDeleteApp_JITWalletWithBalance_ReclaimsToHub covers the generic
// api.DeleteApp path — the one the app detail page's "Disconnect" action
// actually calls (DELETE /api/apps/:id), as opposed to the dedicated
// DeleteJITWallet endpoint above. Before this was fixed, DeleteApp fell
// straight through to apps.DeleteApp, which has no reclaim logic for
// jit_wallet and would have silently destroyed the remaining balance instead
// of returning it to the hub.
func TestDeleteApp_JITWalletWithBalance_ReclaimsToHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).Pubkey = selfPaymentPubkey

	hub := tests.CreateJITHub(t, svc, 300_000, 3600)
	pk := tests.RandomHex32()
	const fundedMloki = 200_000

	childApp, _, err := svc.AppsService.CreateApp(
		"jit-child", "", fundedMloki/1000, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE}, db.AppKindJITWallet, &hub.ID, db.ParentKindJIT, nil,
	)
	require.NoError(t, err)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(childApp.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk, AmountMloki: fundedMloki},
	}))
	_, err = svc.AppsService.ClaimJITWalletSlice(childApp.ID, db.JITAllocIdentityPubkey, pk)
	require.NoError(t, err)
	tests.FundApp(svc, childApp.ID, fundedMloki, "fundtxhash")

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteApp(childApp)
	require.NoError(t, err)

	var appCount int64
	svc.DB.Model(&db.App{}).Where("id = ?", childApp.ID).Count(&appCount)
	assert.Zero(t, appCount, "the wallet app must be deleted")

	hubBalance := queries.GetIsolatedBalance(svc.DB, hub.ID)
	assert.Greater(t, hubBalance, int64(0), "hub must gain balance after the reclaim transfer, not lose it")
}

func TestDeleteJITWallet_WrongHub_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hubA := tests.CreateJITHub(t, svc, 10_000, 3600)
	hubB := tests.CreateJITHub(t, svc, 10_000, 3600)
	childApp, _, err := svc.AppsService.CreateApp(
		"jit-child", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE}, db.AppKindJITWallet, &hubA.ID, db.ParentKindJIT, nil,
	)
	require.NoError(t, err)

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteJITWallet(hubB.ID, childApp.ID)
	require.Error(t, err, "deleting another hub's wallet must fail")

	var count int64
	svc.DB.Model(&db.App{}).Where("id = ?", childApp.ID).Count(&count)
	assert.Equal(t, int64(1), count, "the wallet must still exist after a wrong-hub delete attempt")
}

func TestDeleteJITWallet_NotJITHub_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	isolatedApp, _, err := svc.AppsService.CreateApp(
		"iso", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteJITWallet(isolatedApp.ID, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a jit_hub")
}
