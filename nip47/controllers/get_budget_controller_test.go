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
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

const nip47GetBudgetJson = `
{
	"method": "get_budget"
}
`

func TestHandleGetBudgetEvent_NoRenewal(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetBudgetJson), nip47Request)
	assert.NoError(t, err)

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId:         app.ID,
		App:           *app,
		Scope:         constants.PAY_INVOICE_SCOPE,
		MaxAmountLoki: 400,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleGetBudgetEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse)

	assert.Equal(t, uint64(400000), publishedResponse.Result.(*getBudgetResponse).TotalBudget)
	assert.Equal(t, uint64(0), publishedResponse.Result.(*getBudgetResponse).UsedBudget)
	assert.Nil(t, publishedResponse.Result.(*getBudgetResponse).RenewsAt)
	assert.Equal(t, constants.BUDGET_RENEWAL_NEVER, publishedResponse.Result.(*getBudgetResponse).RenewalPeriod)
	assert.Nil(t, publishedResponse.Error)
}

func TestHandleGetBudgetEvent_NoneUsed(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetBudgetJson), nip47Request)
	assert.NoError(t, err)

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)
	now := time.Now()

	appPermission := &db.AppPermission{
		AppId:         app.ID,
		App:           *app,
		Scope:         constants.PAY_INVOICE_SCOPE,
		MaxAmountLoki: 400,
		BudgetRenewal: constants.BUDGET_RENEWAL_MONTHLY,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleGetBudgetEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse)

	assert.Equal(t, uint64(400000), publishedResponse.Result.(*getBudgetResponse).TotalBudget)
	assert.Equal(t, uint64(0), publishedResponse.Result.(*getBudgetResponse).UsedBudget)
	renewsAt := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, 1, 0).Unix()
	assert.Equal(t, uint64(renewsAt), *publishedResponse.Result.(*getBudgetResponse).RenewsAt)
	assert.Equal(t, constants.BUDGET_RENEWAL_MONTHLY, publishedResponse.Result.(*getBudgetResponse).RenewalPeriod)
	assert.Nil(t, publishedResponse.Error)
}

func TestHandleGetBudgetEvent_HalfUsed(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetBudgetJson), nip47Request)
	assert.NoError(t, err)

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)
	now := time.Now()

	appPermission := &db.AppPermission{
		AppId:         app.ID,
		App:           *app,
		Scope:         constants.PAY_INVOICE_SCOPE,
		MaxAmountLoki: 400,
		BudgetRenewal: constants.BUDGET_RENEWAL_MONTHLY,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	svc.DB.Create(&db.Transaction{
		AppId:       &app.ID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		AmountMloki: 200000,
	})

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleGetBudgetEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse)

	assert.Equal(t, uint64(400000), publishedResponse.Result.(*getBudgetResponse).TotalBudget)
	assert.Equal(t, uint64(200000), publishedResponse.Result.(*getBudgetResponse).UsedBudget)
	renewsAt := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, 1, 0).Unix()
	assert.Equal(t, uint64(renewsAt), *publishedResponse.Result.(*getBudgetResponse).RenewsAt)
	assert.Equal(t, constants.BUDGET_RENEWAL_MONTHLY, publishedResponse.Result.(*getBudgetResponse).RenewalPeriod)
	assert.Nil(t, publishedResponse.Error)
}

func TestHandleGetBudgetEvent_NoBudget(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetBudgetJson), nip47Request)
	assert.NoError(t, err)

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.PAY_INVOICE_SCOPE,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	svc.DB.Create(&db.Transaction{
		AppId:       &app.ID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		AmountMloki: 200000,
	})

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleGetBudgetEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse)

	assert.Equal(t, struct{}{}, publishedResponse.Result)
	assert.Nil(t, publishedResponse.Error)
}

func TestHandleGetBudgetEvent_NoPayInvoicePermission(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47GetBudgetJson), nip47Request)
	assert.NoError(t, err)

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleGetBudgetEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse)

	assert.Equal(t, struct{}{}, publishedResponse.Result)
	assert.Nil(t, publishedResponse.Error)
}

// get_budget is always-granted (permissions.GetAlwaysGrantedMethods) and
// carries no circle-allowlist/following policy check — reachable by anyone
// holding a circle_hub's raw connection string, authorized or not. This pins
// down that it still leaks nothing about the hub's real committed capacity:
// a circle_hub is never granted pay_invoice/jit_claim_funds (the only scopes
// get_budget's own MaxAmountLoki lookup matches against), even when the hub
// has a real admin-set budget ceiling configured (stored on its
// CIRCLE_WALLET_SCOPE permission row instead — a different scope, deliberately
// not checked here).
func TestHandleGetBudgetEvent_CircleHub_NoLeak(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"circle", "", 500_000, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(nip47GetBudgetJson), nip47Request))
	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(&dbRequestEvent).Error)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).
		HandleGetBudgetEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	assert.Equal(t, struct{}{}, publishedResponse.Result, "a circle_hub's get_budget must reveal nothing about its real committed capacity")
	assert.Nil(t, publishedResponse.Error)
}
