package api

// Covers ListCircleChildrenBalances and DeleteCircleHub: children have no DB
// cascade from their parent circle_hub (App.ParentAppID has no FK), so deleting
// a provider must explicitly handle its children rather than silently orphaning them.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
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

	theAPI := newTestAPIForCircleDelete(svc)
	result, err := theAPI.DeleteCircleHub(provider, db.CircleDeleteModeAll)
	require.NoError(t, err)
	assert.True(t, result.HubDeleted)
	assert.Empty(t, result.DeletedChildIDs)
	assert.Empty(t, result.SkippedChildIDs)

	assert.Nil(t, svc.AppsService.GetAppById(provider.ID))
}

func TestDeleteCircleHub_AllEmpty_ModeAll_DeletesEverything(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	child1 := createCircleChild(t, svc, provider, "child1")
	child2 := createCircleChild(t, svc, provider, "child2")

	theAPI := newTestAPIForCircleDelete(svc)
	result, err := theAPI.DeleteCircleHub(provider, db.CircleDeleteModeAll)
	require.NoError(t, err)
	assert.True(t, result.HubDeleted)
	assert.ElementsMatch(t, []uint{child1.ID, child2.ID}, result.DeletedChildIDs)
	assert.Empty(t, result.SkippedChildIDs)

	assert.Nil(t, svc.AppsService.GetAppById(provider.ID))
	assert.Nil(t, svc.AppsService.GetAppById(child1.ID))
	assert.Nil(t, svc.AppsService.GetAppById(child2.ID))
}

func TestDeleteCircleHub_AllEmpty_ModeEmptyOnly_DeletesEverything(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	child := createCircleChild(t, svc, provider, "child")

	theAPI := newTestAPIForCircleDelete(svc)
	result, err := theAPI.DeleteCircleHub(provider, db.CircleDeleteModeEmptyOnly)
	require.NoError(t, err)
	assert.True(t, result.HubDeleted, "with nothing to preserve, empty_only should fully delete like all")
	assert.Equal(t, []uint{child.ID}, result.DeletedChildIDs)
	assert.Empty(t, result.SkippedChildIDs)

	assert.Nil(t, svc.AppsService.GetAppById(provider.ID))
}

func TestDeleteCircleHub_FundedChild_ModeEmptyOnly_AbortsHubDeletion(t *testing.T) {
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
	tests.FundApp(svc, funded.ID, 75_000, "delete-test-hash-2")

	theAPI := newTestAPIForCircleDelete(svc)
	result, err := theAPI.DeleteCircleHub(provider, db.CircleDeleteModeEmptyOnly)
	require.NoError(t, err)
	assert.False(t, result.HubDeleted, "a nonzero-balance child must abort the provider deletion")
	assert.Equal(t, []uint{empty.ID}, result.DeletedChildIDs)
	assert.Equal(t, []uint{funded.ID}, result.SkippedChildIDs)

	// Provider and the funded child are both left fully intact.
	assert.NotNil(t, svc.AppsService.GetAppById(provider.ID))
	assert.NotNil(t, svc.AppsService.GetAppById(funded.ID))
	assert.Nil(t, svc.AppsService.GetAppById(empty.ID))
}

func TestDeleteCircleHub_FundedChild_ModeAll_DeletesEverythingAnyway(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	funded := createCircleChild(t, svc, provider, "funded-child")
	tests.FundApp(svc, funded.ID, 10_000, "delete-test-hash-3")

	theAPI := newTestAPIForCircleDelete(svc)
	result, err := theAPI.DeleteCircleHub(provider, db.CircleDeleteModeAll)
	require.NoError(t, err)
	assert.True(t, result.HubDeleted)
	assert.Equal(t, []uint{funded.ID}, result.DeletedChildIDs)
	assert.Empty(t, result.SkippedChildIDs)

	assert.Nil(t, svc.AppsService.GetAppById(provider.ID))
	assert.Nil(t, svc.AppsService.GetAppById(funded.ID))
}

// mode="all" is a plain bulk delete with no reclaim, unlike every other
// circle/JIT deletion path (DeleteCircleWalletChild, the periodic expiry
// sweep, mode="empty_only") — a funded child's balance is discarded, not
// swept back to the hub. Both the funded child AND the hub itself are
// deleted in one call here, so there's no surviving row to query balances
// off afterward to prove a reclaim didn't happen (a reclaim's transaction
// rows would cascade-delete right along with the child either way). The
// practical way to pin this down: newTestAPIForCircleDelete builds an *api
// whose svc field is left nil — if mode="all" ever tried to reclaim (which
// goes through svc to reach the LN client/transactions service), calling a
// method on that nil interface would panic immediately. A clean,
// non-panicking success is direct evidence no reclaim was attempted.
func TestDeleteCircleHub_FundedChild_ModeAll_NoReclaimAttempted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	funded := createCircleChild(t, svc, provider, "funded-child")
	tests.FundApp(svc, funded.ID, 25_000, "delete-test-hash-no-reclaim")

	theAPI := newTestAPIForCircleDelete(svc) // svc.svc left nil: a reclaim attempt would panic here
	result, err := theAPI.DeleteCircleHub(provider, db.CircleDeleteModeAll)
	require.NoError(t, err, "mode=all must succeed without ever touching the (nil) money-movement service")
	assert.True(t, result.HubDeleted)
	assert.Equal(t, []uint{funded.ID}, result.DeletedChildIDs)
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
