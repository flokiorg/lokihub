package controllers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

const nip47GetInfoJson = `
{
	"method": "get_info"
}
`

func TestHandleGetInfoEvent_NoPermission(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	lightningAddress := "hello@flokicoin.org"
	svc.Cfg.SetUpdate("LightningAddress", lightningAddress, "")

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetInfoJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	// delete the existing app permissions (the app was created with get_info scope)
	svc.DB.Exec("delete from app_permissions")

	appPermission := &db.AppPermission{
		AppId:     app.ID,
		Scope:     constants.GET_BALANCE_SCOPE,
		ExpiresAt: nil,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleGetInfoEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse)

	assert.Nil(t, publishedResponse.Error)
	nodeInfo := publishedResponse.Result.(*getInfoResponse)
	assert.Nil(t, nodeInfo.Alias)
	assert.Nil(t, nodeInfo.Color)
	assert.Nil(t, nodeInfo.Pubkey)
	assert.Nil(t, nodeInfo.Network)
	assert.Nil(t, nodeInfo.BlockHeight)
	assert.Nil(t, nodeInfo.BlockHash)
	assert.Nil(t, nodeInfo.LightningAddress)
	// get_info method is always granted, but does not return pubkey
	assert.Contains(t, nodeInfo.Methods, models.GET_INFO_METHOD)
	assert.Equal(t, []string{}, nodeInfo.Notifications)
}

func TestHandleGetInfoEvent_SubwalletNoPermission(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	lightningAddress := "hello@flokicoin.org"

	metadata := map[string]interface{}{
		"app_store_app_id": constants.SUBWALLET_APPSTORE_APP_ID,
		"lud16":            lightningAddress,
	}

	svc.Cfg.SetUpdate("LNBackendType", config.FLNDBackendType, "")

	app, _, err := svc.AppsService.CreateApp("test", "", 0, "monthly", nil, []string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", metadata)
	assert.NoError(t, err)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetInfoJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	// delete the existing app permissions (the app was created with get_info scope)
	svc.DB.Exec("delete from app_permissions")

	appPermission := &db.AppPermission{
		AppId:     app.ID,
		Scope:     constants.GET_BALANCE_SCOPE,
		ExpiresAt: nil,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleGetInfoEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse)

	assert.Nil(t, publishedResponse.Error)
	nodeInfo := publishedResponse.Result.(*getInfoResponse)
	assert.Nil(t, nodeInfo.Alias)
	assert.Nil(t, nodeInfo.Color)
	assert.Nil(t, nodeInfo.Pubkey)
	assert.Nil(t, nodeInfo.Network)
	assert.Nil(t, nodeInfo.BlockHeight)
	assert.Nil(t, nodeInfo.BlockHash)
	assert.Nil(t, nodeInfo.LightningAddress)
	// get_info method is always granted, but does not return pubkey
	assert.Contains(t, nodeInfo.Methods, models.GET_INFO_METHOD)
	assert.Equal(t, []string{}, nodeInfo.Notifications)
}

func TestHandleGetInfoEvent_WithPermission(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetInfoJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId:     app.ID,
		Scope:     constants.GET_INFO_SCOPE,
		ExpiresAt: nil,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleGetInfoEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse)

	assert.Nil(t, publishedResponse.Error)
	nodeInfo := publishedResponse.Result.(*getInfoResponse)
	assert.Equal(t, tests.MockNodeInfo.Alias, *nodeInfo.Alias)
	assert.Equal(t, tests.MockNodeInfo.Color, *nodeInfo.Color)
	assert.Equal(t, tests.MockNodeInfo.Pubkey, *nodeInfo.Pubkey)
	assert.Equal(t, tests.MockNodeInfo.Network, *nodeInfo.Network)
	assert.Equal(t, tests.MockNodeInfo.BlockHeight, *nodeInfo.BlockHeight)
	assert.Equal(t, tests.MockNodeInfo.BlockHash, *nodeInfo.BlockHash)
	assert.Contains(t, nodeInfo.Methods, "get_info")
	assert.Equal(t, []string{}, nodeInfo.Notifications)
}

func TestHandleGetInfoEvent_WithMetadata(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	lightningAddress := "hello@flokicoin.org"
	svc.Cfg.SetUpdate("LightningAddress", lightningAddress, "")

	metadata := map[string]interface{}{
		"a": 123,
	}

	app, _, err := svc.AppsService.CreateApp("test", "", 0, "monthly", nil, []string{constants.GET_INFO_SCOPE}, "", nil, "", metadata)
	assert.NoError(t, err)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetInfoJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId:     app.ID,
		Scope:     constants.GET_INFO_SCOPE,
		ExpiresAt: nil,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleGetInfoEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse)

	assert.Nil(t, publishedResponse.Error)
	nodeInfo := publishedResponse.Result.(*getInfoResponse)
	assert.Equal(t, tests.MockNodeInfo.Alias, *nodeInfo.Alias)
	assert.Equal(t, tests.MockNodeInfo.Color, *nodeInfo.Color)
	assert.Equal(t, tests.MockNodeInfo.Pubkey, *nodeInfo.Pubkey)
	assert.Equal(t, tests.MockNodeInfo.Network, *nodeInfo.Network)
	assert.Equal(t, tests.MockNodeInfo.BlockHeight, *nodeInfo.BlockHeight)
	assert.Equal(t, tests.MockNodeInfo.BlockHash, *nodeInfo.BlockHash)
	assert.Equal(t, lightningAddress, *nodeInfo.LightningAddress)
	assert.Contains(t, nodeInfo.Methods, "get_info")
	assert.Equal(t, []string{}, nodeInfo.Notifications)

	assert.NoError(t, err)
	assert.Equal(t, float64(123), nodeInfo.Metadata.(map[string]interface{})["a"])
}

func TestHandleGetInfoEvent_SubwalletWithMetadata(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	lightningAddress := "hello@flokicoin.org"

	metadata := map[string]interface{}{
		"app_store_app_id": constants.SUBWALLET_APPSTORE_APP_ID,
		"lud16":            lightningAddress,
		"a":                123,
	}

	svc.Cfg.SetUpdate("LNBackendType", config.FLNDBackendType, "")
	app, _, err := svc.AppsService.CreateApp("test", "", 0, "monthly", nil, []string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", metadata)
	assert.NoError(t, err)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetInfoJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId:     app.ID,
		Scope:     constants.GET_INFO_SCOPE,
		ExpiresAt: nil,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleGetInfoEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse)

	assert.Nil(t, publishedResponse.Error)
	nodeInfo := publishedResponse.Result.(*getInfoResponse)
	assert.Equal(t, tests.MockNodeInfo.Alias, *nodeInfo.Alias)
	assert.Equal(t, tests.MockNodeInfo.Color, *nodeInfo.Color)
	assert.Equal(t, tests.MockNodeInfo.Pubkey, *nodeInfo.Pubkey)
	assert.Equal(t, tests.MockNodeInfo.Network, *nodeInfo.Network)
	assert.Equal(t, tests.MockNodeInfo.BlockHeight, *nodeInfo.BlockHeight)
	assert.Equal(t, tests.MockNodeInfo.BlockHash, *nodeInfo.BlockHash)
	assert.Equal(t, lightningAddress, *nodeInfo.LightningAddress)
	assert.Contains(t, nodeInfo.Methods, "get_info")
	assert.Equal(t, []string{}, nodeInfo.Notifications)

	assert.NoError(t, err)
	assert.Equal(t, float64(123), nodeInfo.Metadata.(map[string]interface{})["a"])
}

func TestHandleGetInfoEvent_WithNotifications(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetInfoJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId:     app.ID,
		Scope:     constants.GET_INFO_SCOPE,
		ExpiresAt: nil,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	appPermission = &db.AppPermission{
		AppId:     app.ID,
		Scope:     constants.NOTIFICATIONS_SCOPE,
		ExpiresAt: nil,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleGetInfoEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse)

	assert.Nil(t, publishedResponse.Error)
	nodeInfo := publishedResponse.Result.(*getInfoResponse)
	assert.Equal(t, tests.MockNodeInfo.Alias, *nodeInfo.Alias)
	assert.Equal(t, tests.MockNodeInfo.Color, *nodeInfo.Color)
	assert.Equal(t, tests.MockNodeInfo.Pubkey, *nodeInfo.Pubkey)
	assert.Equal(t, tests.MockNodeInfo.Network, *nodeInfo.Network)
	assert.Equal(t, tests.MockNodeInfo.BlockHeight, *nodeInfo.BlockHeight)
	assert.Equal(t, tests.MockNodeInfo.BlockHash, *nodeInfo.BlockHash)
	assert.Contains(t, nodeInfo.Methods, "get_info")
	assert.Equal(t, []string{"payment_received", "payment_sent"}, nodeInfo.Notifications)
}

func TestHandleGetInfoEvent_CircleAdmin_CircleWalletBlock(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.Cfg.SetUpdate("LNBackendType", config.FLNDBackendType, "")

	circleAdmin, _, err := svc.AppsService.CreateCircleHub(
		"Circle Admin",
		"",
		0,
		constants.BUDGET_RENEWAL_NEVER,
		nil,
		[]string{constants.GET_INFO_SCOPE, constants.CIRCLE_WALLET_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "Circle Admin", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{
			MaxExpSecs:        7200,
			FeesPpm:           500,
			PerWalletMaxMloki: 100_000,
		},
	)
	require.NoError(t, err)

	// Pre-fund the circle admin with 200_000 mloki.
	svc.DB.Create(&db.Transaction{
		AppId:       &circleAdmin.ID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		AmountMloki: 200_000,
		PaymentHash: "circlefund",
	})

	// Commit 50_000 mloki via an existing active child wallet.
	future := time.Now().Add(time.Hour)
	_, _, err = svc.AppsService.CreateApp(
		"child1",
		"",
		50, // 50 loki = 50_000 mloki
		constants.BUDGET_RENEWAL_NEVER,
		&future,
		[]string{constants.PAY_INVOICE_SCOPE},
		db.AppKindCircleWallet,
		&circleAdmin.ID,
		db.ParentKindCircle,
		nil,
	)
	require.NoError(t, err)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetInfoJson), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleGetInfoEvent(ctx, nip47Request, dbRequestEvent.ID, circleAdmin, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.Nil(t, publishedResponse.Error)
	nodeInfo := publishedResponse.Result.(*getInfoResponse)
	require.NotNil(t, nodeInfo.CircleWallet, "circle_admin app must have circle_wallet block in get_info response")
	assert.Equal(t, int64(150_000), nodeInfo.CircleWallet.AvailableMloki) // 200k - 50k commitment
	assert.Equal(t, 7200, nodeInfo.CircleWallet.MaxExpSecs)
	assert.Equal(t, 500, nodeInfo.CircleWallet.FeesPpm)
	assert.Equal(t, db.CirclePolicyAllowlist, nodeInfo.CircleWallet.CirclePolicy)
}

func TestHandleGetInfoEvent_CircleAdmin_ZeroAvailableMloki(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.Cfg.SetUpdate("LNBackendType", config.FLNDBackendType, "")

	circleAdmin, _, err := svc.AppsService.CreateCircleHub(
		"Circle Admin Zero",
		"",
		0,
		constants.BUDGET_RENEWAL_NEVER,
		nil,
		[]string{constants.GET_INFO_SCOPE, constants.CIRCLE_WALLET_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "Circle Admin Zero", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	// Pre-fund the circle admin with only 50_000 mloki.
	svc.DB.Create(&db.Transaction{
		AppId:       &circleAdmin.ID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		AmountMloki: 50_000,
		PaymentHash: "circlefund2",
	})

	// Create a child with 80_000 mloki commitment — exceeds balance.
	future := time.Now().Add(time.Hour)
	_, _, err = svc.AppsService.CreateApp(
		"child-over",
		"",
		80, // 80 loki = 80_000 mloki
		constants.BUDGET_RENEWAL_NEVER,
		&future,
		[]string{constants.PAY_INVOICE_SCOPE},
		db.AppKindCircleWallet,
		&circleAdmin.ID,
		db.ParentKindCircle,
		nil,
	)
	require.NoError(t, err)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetInfoJson), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleGetInfoEvent(ctx, nip47Request, dbRequestEvent.ID, circleAdmin, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.Nil(t, publishedResponse.Error)
	nodeInfo := publishedResponse.Result.(*getInfoResponse)
	require.NotNil(t, nodeInfo.CircleWallet)
	// commitment (80k) > balance (50k) → available_mloki must be clamped to 0, not negative.
	assert.Equal(t, int64(0), nodeInfo.CircleWallet.AvailableMloki)
}

func TestHandleGetInfoEvent_NonCircleAdmin_NoCircleWalletBlock(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)

	// Grant get_info permission.
	svc.DB.Create(&db.AppPermission{AppId: app.ID, Scope: constants.GET_INFO_SCOPE})

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetInfoJson), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleGetInfoEvent(ctx, nip47Request, dbRequestEvent.ID, app, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.Nil(t, publishedResponse.Error)
	nodeInfo := publishedResponse.Result.(*getInfoResponse)
	assert.Nil(t, nodeInfo.CircleWallet, "non-circle_admin app must not have circle_wallet block")
}
