package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// selfPaymentPubkey is the destination pubkey embedded in tests.MockInvoice —
// setting the mock LN client's pubkey to this makes an internal transfer's
// incoming leg settle synchronously (recognised as a self-payment), matching
// service/jit_cleanup_service_test.go's setup for the same reason.
const selfPaymentPubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"

// newBareJITWallet creates a jit_wallet child directly (bypassing
// jitwallet.Create), for tests that only need a wallet + claim rows to exist,
// not a real funding transfer.
func newBareJITWallet(t *testing.T, svc *tests.TestService, hub *db.App, maxAmountLoki uint64) *db.App {
	t.Helper()
	wallet, _, err := svc.AppsService.CreateApp(
		"jit-child", "", maxAmountLoki, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindJITWallet, &hub.ID, db.ParentKindJIT, nil,
	)
	require.NoError(t, err)
	return wallet
}

// --- ListJITWalletClaims ---

func TestListJITWalletClaims_EmptyHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 10_000, 3600)
	theAPI := newTestAPI(svc)

	result, _, _, err := theAPI.ListJITWalletClaims(hub.ID, 0, 0, "")
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestListJITWalletClaims_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 10_000, 3600)
	wallet := newBareJITWallet(t, svc, hub, 10)
	pk1 := tests.RandomHex32()
	pk2 := tests.RandomHex32()

	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk1, AmountMloki: 3000},
		{IdentityType: db.JITAllocIdentityConnectionKey, IdentityValue: pk2, AmountMloki: 7000},
	}))

	theAPI := newTestAPI(svc)
	result, _, _, err := theAPI.ListJITWalletClaims(hub.ID, 0, 0, "")
	require.NoError(t, err)
	require.Len(t, result, 2)

	ids := map[string]bool{}
	for _, r := range result {
		ids[r.IdentityValue] = true
		assert.False(t, r.Claimed)
		assert.Nil(t, r.ClaimedAt)
		assert.Equal(t, wallet.ID, r.WalletAppID)
		assert.Greater(t, r.CreatedAt, int64(0))
	}
	assert.True(t, ids[pk1])
	assert.True(t, ids[pk2])
}

func TestListJITWalletClaims_ShowsClaimedStatus(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 10_000, 3600)
	wallet := newBareJITWallet(t, svc, hub, 3)
	pk := tests.RandomHex32()

	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk, AmountMloki: 3000},
	}))
	_, err = svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, pk)
	require.NoError(t, err)

	theAPI := newTestAPI(svc)
	result, _, _, err := theAPI.ListJITWalletClaims(hub.ID, 0, 0, "")
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.True(t, result[0].Claimed)
	assert.NotNil(t, result[0].ClaimedAt)
}

func TestListJITWalletClaims_StatusAndCounts(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 10_000, 3600)
	theAPI := newTestAPI(svc)

	// Unclaimed slice.
	unclaimedWallet := newBareJITWallet(t, svc, hub, 2)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(unclaimedWallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 2000},
	}))

	// Claimed slice.
	claimedWallet := newBareJITWallet(t, svc, hub, 3)
	claimedPk := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(claimedWallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: claimedPk, AmountMloki: 3000},
	}))
	_, err = svc.AppsService.ClaimJITWalletSlice(claimedWallet.ID, db.JITAllocIdentityPubkey, claimedPk)
	require.NoError(t, err)

	all, totalCount, counts, err := theAPI.ListJITWalletClaims(hub.ID, 0, 0, "")
	require.NoError(t, err)
	assert.Len(t, all, 2)
	assert.EqualValues(t, 2, totalCount)
	assert.EqualValues(t, 2, counts.All)
	assert.EqualValues(t, 1, counts.Unclaimed)
	assert.EqualValues(t, 1, counts.Claimed)

	unclaimedOnly, _, _, err := theAPI.ListJITWalletClaims(hub.ID, 0, 0, JITAllocationStatusUnclaimed)
	require.NoError(t, err)
	assert.Len(t, unclaimedOnly, 1)

	claimedOnly, _, _, err := theAPI.ListJITWalletClaims(hub.ID, 0, 0, JITAllocationStatusClaimed)
	require.NoError(t, err)
	assert.Len(t, claimedOnly, 1)
}

func TestListJITWalletClaims_NotJITHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	isolatedApp, _, err := svc.AppsService.CreateApp(
		"iso", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)

	theAPI := newTestAPI(svc)
	_, _, _, err = theAPI.ListJITWalletClaims(isolatedApp.ID, 0, 0, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a jit_hub")
}

// --- DeleteJITWalletClaim ---

func TestDeleteJITWalletClaim_Unclaimed_SweepsBackToHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = selfPaymentPubkey
	// Two distinct canned invoices, not one: this claim is the wallet's only
	// one, so api.DeleteJITWalletClaim's cascade now also reclaims/deletes
	// the now-empty wallet (see its own doc comment) - a second internal
	// transfer, on top of the sweep-back this test already exercised. Reusing
	// the same default canned invoice for both would make the second
	// SendPaymentSync collide with the first's already-SETTLED payment_hash
	// ("this invoice has already been paid"), which is a mock-fixture
	// limitation (MockLn.MakeInvoice ignores amount/description and always
	// returns the same canned transaction unless queued), not anything a
	// real LN client would ever do for two genuinely separate invoices - see
	// jitwallet/create_test.go's TestCreate_ConcurrentCreation_.. for the
	// same two-distinct-invoices pattern.
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-a", Amount: 1000},
		{Type: "incoming", Invoice: tests.MockZeroAmountInvoice, PaymentHash: tests.MockZeroAmountPaymentHash, Preimage: "preimage-b", Amount: 1000},
	}

	hub := tests.CreateJITHub(t, svc, 300_000, 3600)
	wallet := newBareJITWallet(t, svc, hub, 200)
	pk := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk, AmountMloki: 200_000},
	}))
	tests.FundApp(svc, wallet.ID, 200_000, "fund-wallet")

	var claim db.JITWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", wallet.ID).First(&claim).Error)

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteJITWalletClaim(hub.ID, wallet.ID, claim.ID)
	require.NoError(t, err)

	var count int64
	svc.DB.Model(&db.JITWalletClaim{}).Where("id = ?", claim.ID).Count(&count)
	assert.Zero(t, count, "the claim row must be deleted")

	var walletCount int64
	svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Count(&walletCount)
	assert.Zero(t, walletCount, "removing the wallet's last remaining claim must also reclaim/delete the now-empty wallet itself")
}

// TestDeleteJITWalletClaim_OneOfMultiple_WalletSurvives is the boundary case
// TestDeleteJITWalletClaim_Unclaimed_SweepsBackToHub's cascade doesn't
// apply to: removing one recipient's slice from an otherwise-live shared
// wallet must leave the wallet itself (and its other recipient's slice)
// alone - only the wallet's *last* claim triggers the cascade delete.
func TestDeleteJITWalletClaim_OneOfMultiple_WalletSurvives(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).Pubkey = selfPaymentPubkey

	hub := tests.CreateJITHub(t, svc, 300_000, 3600)
	wallet := newBareJITWallet(t, svc, hub, 200)
	pkRemoved := tests.RandomHex32()
	pkRemaining := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pkRemoved, AmountMloki: 100_000},
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pkRemaining, AmountMloki: 100_000},
	}))
	tests.FundApp(svc, wallet.ID, 200_000, "fund-wallet")

	var claimToRemove db.JITWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ? AND identity_value = ?", wallet.ID, pkRemoved).First(&claimToRemove).Error)

	theAPI := newTestAPIWithService(t, svc)
	require.NoError(t, theAPI.DeleteJITWalletClaim(hub.ID, wallet.ID, claimToRemove.ID))

	var removedCount int64
	svc.DB.Model(&db.JITWalletClaim{}).Where("id = ?", claimToRemove.ID).Count(&removedCount)
	assert.Zero(t, removedCount, "the removed claim row must be gone")

	var remainingCount int64
	svc.DB.Model(&db.JITWalletClaim{}).Where("wallet_app_id = ? AND identity_value = ?", wallet.ID, pkRemaining).Count(&remainingCount)
	assert.EqualValues(t, 1, remainingCount, "the other recipient's slice must be untouched")

	var walletCount int64
	svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Count(&walletCount)
	assert.EqualValues(t, 1, walletCount, "the wallet itself must survive while it still has a live recipient")
}

func TestDeleteJITWalletClaim_AlreadyClaimed_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 10_000, 3600)
	wallet := newBareJITWallet(t, svc, hub, 3)
	pk := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk, AmountMloki: 3000},
	}))
	_, err = svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, pk)
	require.NoError(t, err)

	var claim db.JITWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", wallet.ID).First(&claim).Error)

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteJITWalletClaim(hub.ID, wallet.ID, claim.ID)
	require.Error(t, err, "deleting an already-claimed slice must be rejected")

	var count int64
	svc.DB.Model(&db.JITWalletClaim{}).Where("id = ?", claim.ID).Count(&count)
	assert.EqualValues(t, 1, count, "the claim row must be untouched")
}

func TestDeleteJITWalletClaim_NotJITHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	isolatedApp, _, err := svc.AppsService.CreateApp(
		"iso", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteJITWalletClaim(isolatedApp.ID, 1, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a jit_hub")
}

func TestDeleteJITWalletClaim_NotJITWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 10_000, 3600)
	isolatedApp, _, err := svc.AppsService.CreateApp(
		"iso", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteJITWalletClaim(hub.ID, isolatedApp.ID, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JIT wallet not found for this hub")
}

// TestDeleteJITWalletClaim_WrongHub_Rejected is the regression test for the
// cross-hub deletion bug found in code review: api.DeleteJITWalletClaim used
// to take only (walletAppID, claimID), with no check that walletAppID
// actually belonged to the hub named in the request's URL - so a caller
// scoped to hub A could delete (and redirect the sweep-back of) a claim that
// actually belonged to a completely unrelated hub B, simply by supplying
// hub B's walletId/claimId while hitting hub A's endpoint. hubAppID is now a
// required parameter, checked the same way DeleteJITWallet already checks it.
func TestDeleteJITWalletClaim_WrongHub_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hubA := tests.CreateJITHub(t, svc, 10_000, 3600)
	hubB := tests.CreateJITHub(t, svc, 10_000, 3600)
	walletB := newBareJITWallet(t, svc, hubB, 3)
	pk := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(walletB.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk, AmountMloki: 3000},
	}))

	var claim db.JITWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", walletB.ID).First(&claim).Error)

	theAPI := newTestAPIWithService(t, svc)
	// hubA's URL, but walletB/claim actually belong to hubB.
	err = theAPI.DeleteJITWalletClaim(hubA.ID, walletB.ID, claim.ID)
	require.Error(t, err, "a claim belonging to a different hub must not be deletable through this hub's endpoint")
	assert.Contains(t, err.Error(), "not found for this hub")

	var count int64
	svc.DB.Model(&db.JITWalletClaim{}).Where("id = ?", claim.ID).Count(&count)
	assert.EqualValues(t, 1, count, "the claim must be untouched")
}

// --- GetJITWalletConnection ---

func TestGetJITWalletConnection_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 10_000, 3600)
	wallet := newBareJITWallet(t, svc, hub, 0)

	theAPI := newTestAPI(svc)
	conn, err := theAPI.GetJITWalletConnection(wallet.ID)
	require.NoError(t, err)
	assert.Contains(t, conn.PairingURI, "nostr+walletconnect://")
	assert.Contains(t, conn.PairingURI, "&secret=")

	// Deterministic: re-deriving must return the exact same URI every time.
	again, err := theAPI.GetJITWalletConnection(wallet.ID)
	require.NoError(t, err)
	assert.Equal(t, conn.PairingURI, again.PairingURI)
}

func TestGetJITWalletConnection_NotJITWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	isolatedApp, _, err := svc.AppsService.CreateApp(
		"iso", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)

	theAPI := newTestAPI(svc)
	_, err = theAPI.GetJITWalletConnection(isolatedApp.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a jit_wallet")
}
