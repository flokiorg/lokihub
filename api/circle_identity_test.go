package api

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

func TestListCircleIdentities_ReturnsSummaries(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, err = svc.AppsService.CreateCircleIdentity("family", db.CirclePolicyAllowlist, "")
	require.NoError(t, err)

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}
	identities, err := theAPI.ListCircleIdentities()
	require.NoError(t, err)
	require.Len(t, identities, 1)
	assert.Equal(t, "family", identities[0].Name)
}

func TestGetCircleIdentity_AllowlistPolicy_ReturnsPubkeysAndUsedByCount(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	first := createTestCircleHub(t, svc, db.CirclePolicyAllowlist)
	firstCfg, err := svc.AppsService.GetCircleHubConfig(first.ID)
	require.NoError(t, err)

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}
	pk := tests.RandomHex32()
	require.NoError(t, theAPI.ReplaceCircleAllowlist(first, []string{pk}))

	resp, err := theAPI.GetCircleIdentity(context.TODO(), firstCfg.CircleIdentityID)
	require.NoError(t, err)
	assert.Equal(t, []string{pk}, resp.AllowlistPubkeys)
	assert.Equal(t, 1, resp.AllowlistCount)
	assert.Equal(t, 1, resp.UsedByCount)

	// A second provider sharing the identity bumps UsedByCount.
	_, _, err = svc.AppsService.CreateCircleHub(
		"circle-b", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{ExistingID: &firstCfg.CircleIdentityID},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	resp2, err := theAPI.GetCircleIdentity(context.TODO(), firstCfg.CircleIdentityID)
	require.NoError(t, err)
	assert.Equal(t, 2, resp2.UsedByCount, "a second provider referencing the same identity must bump UsedByCount")
}

func TestGetCircleIdentity_NotFound(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}
	_, err = theAPI.GetCircleIdentity(context.TODO(), 999999)
	require.Error(t, err)
}

func TestGetApp_CircleHub_EnrichedWithCircleIdentity(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createTestCircleHub(t, svc, db.CirclePolicyAllowlist)
	cfg, err := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)

	pk := tests.RandomHex32()
	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService, keys: svc.Keys}
	require.NoError(t, theAPI.ReplaceCircleAllowlist(provider, []string{pk}))

	apiApp := theAPI.GetApp(context.TODO(), provider)
	require.NotNil(t, apiApp.CircleIdentity)
	assert.Equal(t, cfg.CircleIdentityID, apiApp.CircleIdentity.ID)
	assert.Equal(t, db.CirclePolicyAllowlist, apiApp.CircleIdentity.Policy)
	assert.Equal(t, 1, apiApp.CircleIdentity.AllowlistCount)
}

func TestGetApp_NonCircleHub_NoCircleIdentity(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := svc.AppsService.CreateApp("standard", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindStandard, nil, "", nil)
	require.NoError(t, err)

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService, keys: svc.Keys}
	apiApp := theAPI.GetApp(context.TODO(), app)
	assert.Nil(t, apiApp.CircleIdentity)
}
