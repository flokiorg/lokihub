package apps_test

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"testing"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// randomHex32 generates a random 64-char hex string (valid pubkey / connection_key shape).
func randomHex32() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// newJITHub creates a jit_hub app with the given per-wallet (total) limits.
func newJITHub(t *testing.T, svc *tests.TestService, perWalletMaxMloki, maxExpSecs int) *db.App {
	t.Helper()
	hub, _, err := svc.AppsService.CreateJITHub(
		"test hub",
		"",
		0,
		constants.BUDGET_RENEWAL_NEVER,
		nil,
		[]string{constants.JIT_HUB_SCOPE, constants.PAY_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		db.JITHubConfig{PerWalletMaxMloki: perWalletMaxMloki, MaxExpSecs: maxExpSecs},
	)
	require.NoError(t, err)
	return hub
}

// newJITWallet creates a bare jit_wallet child app (bypassing jitwallet.Create,
// since these tests only exercise the claim-row plumbing, not the funding flow).
func newJITWallet(t *testing.T, svc *tests.TestService, hub *db.App) *db.App {
	t.Helper()
	wallet, _, err := svc.AppsService.CreateApp(
		"jit-wallet", "", 1, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindJITWallet, &hub.ID, db.ParentKindJIT, nil,
	)
	require.NoError(t, err)
	return wallet
}

// --- CreateJITWalletClaims ---

func TestCreateJITWalletClaims_HappyPath_MixedIdentityTypes(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	wallet := newJITWallet(t, svc, hub)

	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 1000},
		{IdentityType: db.JITAllocIdentityConnectionKey, IdentityValue: randomHex32(), IAPubkey: randomHex32(), AmountMloki: 2000},
	}))

	var count int64
	svc.DB.Model(&db.JITWalletClaim{}).Where("wallet_app_id = ?", wallet.ID).Count(&count)
	assert.Equal(t, int64(2), count)
}

func TestCreateJITWalletClaims_EmptyRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	wallet := newJITWallet(t, svc, hub)

	err = svc.AppsService.CreateJITWalletClaims(wallet.ID, nil)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}

// --- ListJITHubWalletChildren / ListJITWalletClaims ---

func TestListJITHubWalletChildren_IsolatedByHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hubA := newJITHub(t, svc, 10_000, 3600)
	hubB := newJITHub(t, svc, 10_000, 3600)
	newJITWallet(t, svc, hubA)
	newJITWallet(t, svc, hubA)

	childrenA, err := svc.AppsService.ListJITHubWalletChildren(hubA.ID)
	require.NoError(t, err)
	assert.Len(t, childrenA, 2)

	childrenB, err := svc.AppsService.ListJITHubWalletChildren(hubB.ID)
	require.NoError(t, err)
	assert.Empty(t, childrenB)
}

func TestListJITWalletClaims_AcrossMultipleWallets(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	walletA := newJITWallet(t, svc, hub)
	walletB := newJITWallet(t, svc, hub)

	require.NoError(t, svc.AppsService.CreateJITWalletClaims(walletA.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 1000},
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 1500},
	}))
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(walletB.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 2000},
	}))

	rows, err := svc.AppsService.ListJITWalletClaims(hub.ID)
	require.NoError(t, err)
	require.Len(t, rows, 3)
}

func TestListJITWalletClaims_IsolatedByHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hubA := newJITHub(t, svc, 10_000, 3600)
	hubB := newJITHub(t, svc, 10_000, 3600)
	walletA := newJITWallet(t, svc, hubA)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(walletA.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 1000},
	}))

	rowsB, err := svc.AppsService.ListJITWalletClaims(hubB.ID)
	require.NoError(t, err)
	assert.Empty(t, rowsB, "hub B must not see hub A's claims")
}

// --- GetJITWalletClaim ---

func TestGetJITWalletClaim_FoundWhenUnclaimed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	wallet := newJITWallet(t, svc, hub)
	pubkey := randomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pubkey, AmountMloki: 4000},
	}))

	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, pubkey)
	require.NoError(t, err)
	require.NotNil(t, claim)
	assert.Equal(t, int64(4000), claim.AmountMloki)
}

func TestGetJITWalletClaim_NotFound(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	wallet := newJITWallet(t, svc, hub)

	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, randomHex32())
	require.NoError(t, err)
	assert.Nil(t, claim)
}

func TestGetJITWalletClaim_NilOnceClaimed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	wallet := newJITWallet(t, svc, hub)
	pubkey := randomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pubkey, AmountMloki: 1000},
	}))
	_, err = svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, pubkey)
	require.NoError(t, err)

	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, pubkey)
	require.NoError(t, err)
	assert.Nil(t, claim, "a claimed slice must no longer be returned by the unclaimed-only lookup")
}

func TestGetJITWalletClaim_WrongWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	walletA := newJITWallet(t, svc, hub)
	walletB := newJITWallet(t, svc, hub)
	pubkey := randomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(walletA.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pubkey, AmountMloki: 1000},
	}))

	claim, err := svc.AppsService.GetJITWalletClaim(walletB.ID, db.JITAllocIdentityPubkey, pubkey)
	require.NoError(t, err)
	assert.Nil(t, claim, "a claim on wallet A must not be visible when queried against wallet B")
}

// --- ClaimJITWalletSlice ---

func TestClaimJITWalletSlice_Success(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	wallet := newJITWallet(t, svc, hub)
	pubkey := randomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pubkey, AmountMloki: 3000},
	}))

	amount, err := svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, pubkey)
	require.NoError(t, err)
	assert.Equal(t, int64(3000), amount)

	var claim db.JITWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ? AND identity_value = ?", wallet.ID, pubkey).First(&claim).Error)
	assert.NotNil(t, claim.ClaimedAt)
}

func TestClaimJITWalletSlice_AlreadyClaimed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	wallet := newJITWallet(t, svc, hub)
	pubkey := randomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pubkey, AmountMloki: 1000},
	}))

	_, err = svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, pubkey)
	require.NoError(t, err)
	_, err = svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, pubkey)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestClaimJITWalletSlice_NeverExisted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	wallet := newJITWallet(t, svc, hub)

	_, err = svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, randomHex32())
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}

// TestClaimJITWalletSlice_ConcurrentRace_ExactlyOneWinner is the security-
// critical invariant behind claim_funds: two concurrent claims for the SAME
// identity must never both succeed, so a recipient's slice is paid out at
// most once regardless of request timing/replay.
func TestClaimJITWalletSlice_ConcurrentRace_ExactlyOneWinner(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	wallet := newJITWallet(t, svc, hub)
	pubkey := randomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pubkey, AmountMloki: 5000},
	}))

	const goroutines = 5
	errs := make(chan error, goroutines)
	ready := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			_, err := svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, pubkey)
			errs <- err
		}()
	}
	close(ready)
	wg.Wait()
	close(errs)

	var successes, failures int
	for e := range errs {
		if e == nil {
			successes++
		} else {
			failures++
			assert.ErrorIs(t, e, constants.ErrInvalidParams)
		}
	}
	assert.Equal(t, 1, successes, "exactly one goroutine must win the claim")
	assert.Equal(t, goroutines-1, failures)
}

// --- UnclaimJITWalletSlice ---

func TestUnclaimJITWalletSlice_MakesSliceClaimableAgain(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	wallet := newJITWallet(t, svc, hub)
	pubkey := randomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pubkey, AmountMloki: 1000},
	}))
	_, err = svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, pubkey)
	require.NoError(t, err)

	require.NoError(t, svc.AppsService.UnclaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, pubkey))

	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, pubkey)
	require.NoError(t, err)
	require.NotNil(t, claim, "slice must be claimable again after a rollback")

	// Re-claiming must succeed too, not just the lookup.
	amount, err := svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, pubkey)
	require.NoError(t, err)
	assert.Equal(t, int64(1000), amount)
}

// --- DeleteJITWalletClaim ---

func TestDeleteJITWalletClaim_Unclaimed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	wallet := newJITWallet(t, svc, hub)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 1000},
	}))

	var claim db.JITWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", wallet.ID).First(&claim).Error)

	deleted, err := svc.AppsService.DeleteJITWalletClaim(wallet.ID, claim.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1000), deleted.AmountMloki)

	var count int64
	svc.DB.Model(&db.JITWalletClaim{}).Where("id = ?", claim.ID).Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestDeleteJITWalletClaim_AlreadyClaimed_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	wallet := newJITWallet(t, svc, hub)
	pubkey := randomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pubkey, AmountMloki: 1000},
	}))
	_, err = svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, pubkey)
	require.NoError(t, err)

	var claim db.JITWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", wallet.ID).First(&claim).Error)

	_, err = svc.AppsService.DeleteJITWalletClaim(wallet.ID, claim.ID)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestDeleteJITWalletClaim_WrongWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	walletA := newJITWallet(t, svc, hub)
	walletB := newJITWallet(t, svc, hub)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(walletA.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: randomHex32(), AmountMloki: 1000},
	}))

	var claim db.JITWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", walletA.ID).First(&claim).Error)

	_, err = svc.AppsService.DeleteJITWalletClaim(walletB.ID, claim.ID)
	assert.Error(t, err)
}

// --- ListClaimsForWallet ---

func TestListClaimsForWallet_ReturnsClaimedAndUnclaimed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newJITHub(t, svc, 10_000, 3600)
	wallet := newJITWallet(t, svc, hub)
	pubkeyClaimed := randomHex32()
	pubkeyUnclaimed := randomHex32()
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pubkeyClaimed, AmountMloki: 1000},
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pubkeyUnclaimed, AmountMloki: 2000},
	}))
	_, err = svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, pubkeyClaimed)
	require.NoError(t, err)

	claims, err := svc.AppsService.ListClaimsForWallet(wallet.ID)
	require.NoError(t, err)
	require.Len(t, claims, 2)
	for _, c := range claims {
		if c.IdentityValue == pubkeyClaimed {
			assert.NotNil(t, c.ClaimedAt)
		} else {
			assert.Nil(t, c.ClaimedAt)
		}
	}
}
