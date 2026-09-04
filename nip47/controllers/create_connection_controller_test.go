package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

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

func TestHandleCreateConnectionEvent(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	require.NoError(t, svc.Cfg.SetUpdate("LNBackendType", config.FLNDBackendType, ""))

	pairingSecretKey := nostr.GeneratePrivateKey()
	pairingPublicKey, err := nostr.GetPublicKey(pairingSecretKey)
	require.NoError(t, err)

	nip47CreateConnectionJson := fmt.Sprintf(`
{
	"method": "create_connection",
	"params": {
		"pubkey": "%s",
		"name": "Test 123",
		"request_methods": ["get_info", "pay_invoice"],
		"notification_types": ["payment_received"],
		"max_amount": 100000000,
		"budget_renewal": "monthly",
		"kind": "isolated"
	}
}
`, pairingPublicKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47CreateConnectionJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleCreateConnectionEvent(ctx, nip47Request, dbRequestEvent.ID, publishResponse)

	assert.Nil(t, publishedResponse.Error)
	assert.Equal(t, models.CREATE_CONNECTION_METHOD, publishedResponse.ResultType)
	createAppResult := publishedResponse.Result.(createConnectionResponse)

	assert.NotNil(t, createAppResult.WalletPubkey)
	app := db.App{}
	err = svc.DB.First(&app).Error
	assert.NoError(t, err)
	assert.Equal(t, pairingPublicKey, app.AppPubkey)
	assert.Equal(t, createAppResult.WalletPubkey, *app.WalletPubkey)

	permissions := []db.AppPermission{}
	err = svc.DB.Find(&permissions).Error
	assert.NoError(t, err)
	assert.Equal(t, 3, len(permissions))
	assert.Equal(t, constants.GET_INFO_SCOPE, permissions[0].Scope)
	assert.Equal(t, constants.PAY_INVOICE_SCOPE, permissions[1].Scope)
	assert.Equal(t, constants.NOTIFICATIONS_SCOPE, permissions[2].Scope)

	assert.Equal(t, db.AppKindIsolated, app.Kind)
	assert.Equal(t, 100_000, permissions[1].MaxAmountLoki)
	assert.Equal(t, constants.BUDGET_RENEWAL_MONTHLY, permissions[1].BudgetRenewal)
}

func TestHandleCreateConnectionEvent_PubkeyAlreadyExists(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	pairingSecretKey := nostr.GeneratePrivateKey()
	pairingPublicKey, err := nostr.GetPublicKey(pairingSecretKey)
	require.NoError(t, err)

	appsSvc := apps.NewAppsService(svc.DB, svc.EventPublisher, svc.Keys, svc.Cfg)
	_, _, err = appsSvc.CreateApp("Existing App", pairingPublicKey, 0, constants.BUDGET_RENEWAL_NEVER, nil, []string{models.GET_INFO_METHOD}, "", nil, "", nil)
	require.NoError(t, err)

	nip47CreateConnectionJson := fmt.Sprintf(`
{
	"method": "create_connection",
	"params": {
		"pubkey": "%s",
		"name": "Test 123",
		"request_methods": ["get_info"]
	}
}
`, pairingPublicKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47CreateConnectionJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleCreateConnectionEvent(ctx, nip47Request, dbRequestEvent.ID, publishResponse)

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_INTERNAL, publishedResponse.Error.Code)
	assert.Equal(t, "duplicated key not allowed", publishedResponse.Error.Message)
	assert.Equal(t, models.CREATE_CONNECTION_METHOD, publishedResponse.ResultType)
	assert.Nil(t, publishedResponse.Result)
}

func TestHandleCreateConnectionEvent_NoMethods(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	pairingSecretKey := nostr.GeneratePrivateKey()
	pairingPublicKey, err := nostr.GetPublicKey(pairingSecretKey)
	require.NoError(t, err)

	nip47CreateConnectionJson := fmt.Sprintf(`
{
	"method": "create_connection",
	"params": {
		"pubkey": "%s",
		"name": "Test 123"
	}
}
`, pairingPublicKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47CreateConnectionJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleCreateConnectionEvent(ctx, nip47Request, dbRequestEvent.ID, publishResponse)

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
	assert.Equal(t, "No request methods provided", publishedResponse.Error.Message)
	assert.Equal(t, models.CREATE_CONNECTION_METHOD, publishedResponse.ResultType)
	assert.Nil(t, publishedResponse.Result)
}

func TestHandleCreateConnectionEvent_UnsupportedMethod(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	pairingSecretKey := nostr.GeneratePrivateKey()
	pairingPublicKey, err := nostr.GetPublicKey(pairingSecretKey)
	require.NoError(t, err)

	nip47CreateConnectionJson := fmt.Sprintf(`
{
	"method": "create_connection",
	"params": {
		"pubkey": "%s",
		"name": "Test 123",
		"request_methods": ["non_existent"]
	}
}
`, pairingPublicKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47CreateConnectionJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleCreateConnectionEvent(ctx, nip47Request, dbRequestEvent.ID, publishResponse)

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
	assert.Equal(t, "One or more methods are not supported by the current LNClient", publishedResponse.Error.Message)
	assert.Equal(t, models.CREATE_CONNECTION_METHOD, publishedResponse.ResultType)
	assert.Nil(t, publishedResponse.Result)
}

func TestHandleCreateConnectionEvent_UnsupportedNotificationType(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	pairingSecretKey := nostr.GeneratePrivateKey()
	pairingPublicKey, err := nostr.GetPublicKey(pairingSecretKey)
	require.NoError(t, err)

	nip47CreateConnectionJson := fmt.Sprintf(`
{
	"method": "create_connection",
	"params": {
		"pubkey": "%s",
		"name": "Test 123",
		"request_methods": ["get_info"],
		"notification_types": ["non_existent"]
	}
}
`, pairingPublicKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47CreateConnectionJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleCreateConnectionEvent(ctx, nip47Request, dbRequestEvent.ID, publishResponse)

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
	assert.Equal(t, "One or more notification types are not supported by the current LNClient", publishedResponse.Error.Message)
	assert.Equal(t, models.CREATE_CONNECTION_METHOD, publishedResponse.ResultType)
	assert.Nil(t, publishedResponse.Result)
}

func TestHandleCreateConnectionEvent_DoNotAllowCreateConnectionMethod(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	pairingSecretKey := nostr.GeneratePrivateKey()
	pairingPublicKey, err := nostr.GetPublicKey(pairingSecretKey)
	require.NoError(t, err)

	nip47CreateConnectionJson := fmt.Sprintf(`
{
	"method": "create_connection",
	"params": {
		"pubkey": "%s",
		"name": "Test 123",
		"request_methods": ["create_connection"]
	}
}
`, pairingPublicKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47CreateConnectionJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleCreateConnectionEvent(ctx, nip47Request, dbRequestEvent.ID, publishResponse)

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
	assert.Equal(t, "cannot create a new app that has create_connection permission via NWC", publishedResponse.Error.Message)
	assert.Equal(t, models.CREATE_CONNECTION_METHOD, publishedResponse.ResultType)
	assert.Nil(t, publishedResponse.Result)
}

// Fixed 2026-08-31 (companion to the Circle Wallet fix in
// create_circle_wallet_controller.go): a nonzero max_amount in [1, 999]
// mloki used to floor to a stored budget cap of 0, which validateCanPay
// treats as "no cap at all" - the same silent-bypass shape as the Circle
// Wallet bug. Unlike create_circle_wallet, max_amount: 0 itself stays legal
// here (it's this codebase's established "no budget cap" convention for
// generic connections) - only the truncating [1, 999] range is rejected.
func TestHandleCreateConnectionEvent_SubLokiMaxAmount_Rejected(t *testing.T) {
	for _, maxAmount := range []uint64{1, 500, 999} {
		t.Run(fmt.Sprintf("max_amount_%d", maxAmount), func(t *testing.T) {
			ctx := context.TODO()
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			pairingSecretKey := nostr.GeneratePrivateKey()
			pairingPublicKey, err := nostr.GetPublicKey(pairingSecretKey)
			require.NoError(t, err)

			nip47CreateConnectionJson := fmt.Sprintf(`
{
	"method": "create_connection",
	"params": {
		"pubkey": "%s",
		"name": "Test 123",
		"request_methods": ["get_info", "pay_invoice"],
		"max_amount": %d,
		"kind": "isolated"
	}
}
`, pairingPublicKey, maxAmount)

			nip47Request := &models.Request{}
			require.NoError(t, json.Unmarshal([]byte(nip47CreateConnectionJson), nip47Request))

			dbRequestEvent := &db.RequestEvent{}
			require.NoError(t, svc.DB.Create(&dbRequestEvent).Error)

			var publishedResponse *models.Response
			publishResponse := func(response *models.Response, tags nostr.Tags) {
				publishedResponse = response
			}

			NewTestNip47Controller(svc).
				HandleCreateConnectionEvent(ctx, nip47Request, dbRequestEvent.ID, publishResponse)

			require.NotNil(t, publishedResponse.Error,
				"a max_amount below 1000 mloki must be rejected outright, never silently truncated to an unlimited cap")
			assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)

			var count int64
			require.NoError(t, svc.DB.Model(&db.App{}).Where("app_pubkey = ?", pairingPublicKey).Count(&count).Error)
			assert.Equal(t, int64(0), count, "a rejected request must not leave a partially-created app behind")
		})
	}
}

// Control: max_amount: 0 is NOT rejected by the fix above - it's a
// deliberate, pre-existing "unlimited" request, distinct from an accidental
// sub-loki truncation.
func TestHandleCreateConnectionEvent_ZeroMaxAmount_StillMeansUnlimited(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	pairingSecretKey := nostr.GeneratePrivateKey()
	pairingPublicKey, err := nostr.GetPublicKey(pairingSecretKey)
	require.NoError(t, err)

	nip47CreateConnectionJson := fmt.Sprintf(`
{
	"method": "create_connection",
	"params": {
		"pubkey": "%s",
		"name": "Test 123",
		"request_methods": ["get_info", "pay_invoice"],
		"max_amount": 0,
		"kind": "isolated"
	}
}
`, pairingPublicKey)

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(nip47CreateConnectionJson), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(&dbRequestEvent).Error)

	var publishedResponse *models.Response
	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleCreateConnectionEvent(ctx, nip47Request, dbRequestEvent.ID, publishResponse)

	require.Nil(t, publishedResponse.Error)
	app := db.App{}
	require.NoError(t, svc.DB.Where("app_pubkey = ?", pairingPublicKey).First(&app).Error)
	permissions := []db.AppPermission{}
	require.NoError(t, svc.DB.Where("app_id = ?", app.ID).Find(&permissions).Error)
	require.Len(t, permissions, 2)
	assert.Equal(t, 0, permissions[1].MaxAmountLoki)
}
