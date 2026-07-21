package apps_test

// DeleteApp has no DB-level cascade for App.ParentAppID, so deleting a
// circle_hub through the generic path would silently orphan its
// circle_wallet children. These tests cover the guard that refuses that
// instead — callers must use the dedicated circle-delete flow (api.DeleteCircleHub).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestDeleteApp_CircleHubWithNoChildren_Succeeds(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	require.NoError(t, svc.AppsService.DeleteApp(provider))
	assert.Nil(t, svc.AppsService.GetAppById(provider.ID))
}

func TestDeleteApp_CircleHubWithChildren_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	child, _, err := svc.AppsService.CreateApp(
		"child", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleWallet,
		&provider.ID, db.ParentKindCircle, nil,
	)
	require.NoError(t, err)

	err = svc.AppsService.DeleteApp(provider)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	// Nothing should have been touched.
	assert.NotNil(t, svc.AppsService.GetAppById(provider.ID))
	assert.NotNil(t, svc.AppsService.GetAppById(child.ID))
}
