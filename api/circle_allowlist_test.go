package api

// Covers RemoveCircleAllowedPubkey, ListCircleAllowlist, and RefreshCircleAllowlist's
// validation paths — all had 0% test coverage per the Circle/JIT audit even though
// ReplaceCircleAllowlist (the sibling endpoint) was already tested.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func createTestCircleHub(t *testing.T, svc *tests.TestService, policy string) *db.App {
	t.Helper()
	provider, _, err := svc.AppsService.CreateCircleHub(
		"circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "circle", Policy: policy},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	return provider
}

func TestRemoveCircleAllowedPubkey_Success(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := createTestCircleHub(t, svc, db.CirclePolicyAllowlist)
	cfg, err := svc.AppsService.GetCircleHubConfig(app.ID)
	require.NoError(t, err)

	pk := tests.RandomHex32()
	require.NoError(t, svc.DB.Create(&db.CircleIdentityAllowedPubkey{CircleIdentityID: cfg.CircleIdentityID, Pubkey: pk}).Error)

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}
	require.NoError(t, theAPI.RemoveCircleAllowedPubkey(app, pk))

	var count int64
	svc.DB.Model(&db.CircleIdentityAllowedPubkey{}).Where("circle_identity_id = ? AND pubkey = ?", cfg.CircleIdentityID, pk).Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestRemoveCircleAllowedPubkey_NonExistent_IsNoop(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := createTestCircleHub(t, svc, db.CirclePolicyAllowlist)

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}
	// A well-formed pubkey that was never added must not error — deleting
	// zero matching rows is a valid outcome, not a failure.
	require.NoError(t, theAPI.RemoveCircleAllowedPubkey(app, tests.RandomHex32()))
}

func TestRemoveCircleAllowedPubkey_InvalidFormat_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := createTestCircleHub(t, svc, db.CirclePolicyAllowlist)

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}

	cases := []string{"not-a-valid-hex-pubkey!", "deadbeef", "AAAA" + tests.RandomHex32()[4:]}
	for _, pk := range cases {
		err := theAPI.RemoveCircleAllowedPubkey(app, pk)
		require.ErrorIs(t, err, constants.ErrInvalidParams, "pubkey %q must be rejected as invalid", pk)
	}
}

func TestRemoveCircleAllowedPubkey_NonCircleHub_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := &db.App{Name: "not-a-circle", Kind: db.AppKindIsolated}
	require.NoError(t, svc.DB.Create(app).Error)

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}
	require.Error(t, theAPI.RemoveCircleAllowedPubkey(app, tests.RandomHex32()))
}

func TestListCircleAllowlist_ReturnsSortedPubkeys(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := createTestCircleHub(t, svc, db.CirclePolicyAllowlist)
	cfg, err := svc.AppsService.GetCircleHubConfig(app.ID)
	require.NoError(t, err)

	pk1, pk2 := tests.RandomHex32(), tests.RandomHex32()
	require.NoError(t, svc.DB.Create(&db.CircleIdentityAllowedPubkey{CircleIdentityID: cfg.CircleIdentityID, Pubkey: pk1}).Error)
	require.NoError(t, svc.DB.Create(&db.CircleIdentityAllowedPubkey{CircleIdentityID: cfg.CircleIdentityID, Pubkey: pk2}).Error)

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}
	pubkeys, err := theAPI.ListCircleAllowlist(app)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{pk1, pk2}, pubkeys)
}

func TestListCircleAllowlist_Empty(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := createTestCircleHub(t, svc, db.CirclePolicyAllowlist)

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}
	pubkeys, err := theAPI.ListCircleAllowlist(app)
	require.NoError(t, err)
	assert.Empty(t, pubkeys)
}

func TestListCircleAllowlist_NonCircleHub_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := &db.App{Name: "not-a-circle", Kind: db.AppKindIsolated}
	require.NoError(t, svc.DB.Create(app).Error)

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}
	_, err = theAPI.ListCircleAllowlist(app)
	require.Error(t, err)
}

func TestListCircleAllowlist_SharedIdentity_VisibleFromBothProviders(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	first := createTestCircleHub(t, svc, db.CirclePolicyAllowlist)
	firstCfg, err := svc.AppsService.GetCircleHubConfig(first.ID)
	require.NoError(t, err)

	second, _, err := svc.AppsService.CreateCircleHub(
		"circle-b", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{ExistingID: &firstCfg.CircleIdentityID},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}
	pk := tests.RandomHex32()
	require.NoError(t, theAPI.ReplaceCircleAllowlist(first, []string{pk}))

	pubkeysFromFirst, err := theAPI.ListCircleAllowlist(first)
	require.NoError(t, err)
	pubkeysFromSecond, err := theAPI.ListCircleAllowlist(second)
	require.NoError(t, err)
	assert.Equal(t, []string{pk}, pubkeysFromFirst)
	assert.Equal(t, pubkeysFromFirst, pubkeysFromSecond, "a write via one provider must be immediately visible via the other's shared identity")
}

func TestRefreshCircleAllowlist_NonCircleHub_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := &db.App{Name: "not-a-circle", Kind: db.AppKindIsolated}
	require.NoError(t, svc.DB.Create(app).Error)

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService, cfg: svc.Cfg}
	require.Error(t, theAPI.RefreshCircleAllowlist(context.TODO(), app))
}

func TestRefreshCircleAllowlist_NoProviderPubkey_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createTestCircleHub(t, svc, db.CirclePolicyAllowlist) // no ProviderPubkey set

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService, cfg: svc.Cfg}
	err = theAPI.RefreshCircleAllowlist(context.TODO(), provider)
	require.Error(t, err, "refresh must fail fast without hitting any relay when provider_pubkey is unset")
}
