package api

// Covers ListCircleChildrenBalances and DeleteCircleHub: children have no DB
// cascade from their parent circle_hub (App.ParentAppID has no FK), so deleting
// a provider must explicitly handle its children rather than silently orphaning them.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/tests"
)

func newTestAPIForCircleDelete(svc *tests.TestService) *api {
	return &api{db: svc.DB, appsSvc: svc.AppsService, eventPublisher: events.NewEventPublisher()}
}

func createCircleChild(t *testing.T, svc *tests.TestService, parent *db.App, name string) *db.App {
	t.Helper()
	child, _, err := svc.AppsService.CreateApp(
		name, "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleWallet,
		&parent.ID, db.ParentKindCircle, nil,
	)
	require.NoError(t, err)
	return child
}

func TestListCircleChildrenBalances_NoChildren(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	theAPI := newTestAPIForCircleDelete(svc)
	balances, _, err := theAPI.ListCircleChildrenBalances(provider, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, balances)
}

func TestListCircleChildrenBalances_MixedBalances(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	empty := createCircleChild(t, svc, provider, "empty-child")
	funded := createCircleChild(t, svc, provider, "funded-child")
	tests.FundApp(svc, funded.ID, 50_000, "delete-test-hash-1")

	theAPI := newTestAPIForCircleDelete(svc)
	balances, totalCount, err := theAPI.ListCircleChildrenBalances(provider, 0, 0)
	require.NoError(t, err)
	require.Len(t, balances, 2)
	assert.Equal(t, uint64(2), totalCount)

	byID := map[uint]int64{}
	for _, b := range balances {
		byID[b.AppID] = b.BalanceMloki
	}
	assert.Equal(t, int64(0), byID[empty.ID])
	assert.Equal(t, int64(50_000), byID[funded.ID])
}

func TestListCircleChildrenBalances_NonCircleHub_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := &db.App{Name: "standard", Kind: db.AppKindStandard}
	require.NoError(t, svc.DB.Create(app).Error)

	theAPI := newTestAPIForCircleDelete(svc)
	_, _, err = theAPI.ListCircleChildrenBalances(app, 0, 0)
	assert.Error(t, err)
}

func TestDeleteCircleHub_NoChildren(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	theAPI := newTestAPIWithService(t, svc)
	result, err := theAPI.DeleteCircleHub(provider, db.CircleDeleteModeAll)
	require.NoError(t, err)
	assert.True(t, result.HubDeleted)
	assert.Empty(t, result.DeletedChildIDs)
	assert.Empty(t, result.SkippedChildIDs)

	assert.Nil(t, svc.AppsService.GetAppById(provider.ID))
}

func TestDeleteCircleHub_InvalidMode_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	theAPI := newTestAPIForCircleDelete(svc)
	_, err = theAPI.DeleteCircleHub(provider, "not_a_real_mode")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	// Nothing should have been touched.
	assert.NotNil(t, svc.AppsService.GetAppById(provider.ID))
}

func TestDeleteCircleHub_NonCircleHub_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := &db.App{Name: "standard", Kind: db.AppKindStandard}
	require.NoError(t, svc.DB.Create(app).Error)

	theAPI := newTestAPIForCircleDelete(svc)
	_, err = theAPI.DeleteCircleHub(app, db.CircleDeleteModeAll)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}

// Hub teardown (NIP-CW §Lifecycle and Deletion): whatever the mode, every
// wallet moves to Closing, the circle stops accepting joins, and the hub stays
// until its last wallet is deleted (U-16.3).
func TestDeleteCircleHub_TeardownClosesEveryWalletAndKeepsTheHub(t *testing.T) {
	for _, mode := range []string{db.CircleDeleteModeAll, db.CircleDeleteModeEmptyOnly} {
		t.Run(mode, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			provider, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
				[]string{constants.GET_BALANCE_SCOPE}, nil,
				apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
				db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
			)
			require.NoError(t, err)
			empty := createCircleChild(t, svc, provider, "empty")
			funded := createCircleChild(t, svc, provider, "funded")
			tests.FundApp(svc, funded.ID, 75_000, tests.RandomHex32())

			result, err := newTestAPIForCircleDelete(svc).DeleteCircleHub(provider, mode)
			require.NoError(t, err)
			assert.False(t, result.HubDeleted)
			assert.ElementsMatch(t, []uint{empty.ID, funded.ID}, result.ClosingChildIDs)
			assert.NotNil(t, svc.AppsService.GetAppById(provider.ID), "the hub stays while it has wallets")
			for _, id := range []uint{empty.ID, funded.ID} {
				var w db.App
				require.NoError(t, svc.DB.First(&w, id).Error)
				assert.Equal(t, circlelife.StateClosing, w.CircleState)
			}
			assert.Equal(t, int64(75_000), queries.GetIsolatedBalance(svc.DB, funded.ID), "nothing is swept")

			cfg, err := svc.AppsService.GetCircleHubConfig(provider.ID)
			require.NoError(t, err)
			require.NotNil(t, cfg.TeardownRequestedAt)
			require.NotNil(t, cfg.AcceptingJoins)
			assert.False(t, *cfg.AcceptingJoins, "a torn-down circle takes no joins")
		})
	}
}
