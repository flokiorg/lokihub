package permissions

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

func TestHasPermission_NoPermission(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	permissionsSvc := NewPermissionsService(svc.DB, svc.EventPublisher)
	result, code, message := permissionsSvc.HasPermission(app, constants.PAY_INVOICE_SCOPE)
	assert.False(t, result)
	assert.Equal(t, constants.ERROR_RESTRICTED, code)
	assert.Equal(t, "This app does not have the pay_invoice scope", message)
}

func TestHasPermission_Expired(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	budgetRenewal := "never"
	expiresAt := time.Now().Add(-24 * time.Hour)
	appPermission := &db.AppPermission{
		AppId:         app.ID,
		App:           *app,
		Scope:         constants.PAY_INVOICE_SCOPE,
		MaxAmountLoki: 100,
		BudgetRenewal: budgetRenewal,
		ExpiresAt:     &expiresAt,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	permissionsSvc := NewPermissionsService(svc.DB, svc.EventPublisher)
	result, code, message := permissionsSvc.HasPermission(app, constants.PAY_INVOICE_SCOPE)
	assert.False(t, result)
	assert.Equal(t, constants.ERROR_EXPIRED, code)
	assert.Equal(t, "This app has expired", message)
}

func TestHasPermission_OK(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	budgetRenewal := "never"
	expiresAt := time.Now().Add(24 * time.Hour)
	appPermission := &db.AppPermission{
		AppId:         app.ID,
		App:           *app,
		Scope:         constants.PAY_INVOICE_SCOPE,
		MaxAmountLoki: 10,
		BudgetRenewal: budgetRenewal,
		ExpiresAt:     &expiresAt,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	permissionsSvc := NewPermissionsService(svc.DB, svc.EventPublisher)
	result, code, message := permissionsSvc.HasPermission(app, constants.PAY_INVOICE_SCOPE)
	assert.True(t, result)
	assert.Empty(t, code)
	assert.Empty(t, message)
}

func TestRequestMethodToScope_GetBudget(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	assert.NoError(t, err)
	defer svc.Remove()

	scope, err := RequestMethodToScope(models.GET_BUDGET_METHOD)
	assert.NoError(t, err)
	assert.Equal(t, "", scope)
}

func TestRequestMethodsToScopes_GetBudget(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	assert.NoError(t, err)
	defer svc.Remove()

	scopes, err := RequestMethodsToScopes([]string{models.GET_BUDGET_METHOD})
	assert.NoError(t, err)
	assert.Equal(t, []string{}, scopes)
}

func TestRequestMethodToScope_GetInfo(t *testing.T) {
	scope, err := RequestMethodToScope(models.GET_INFO_METHOD)
	assert.NoError(t, err)
	assert.Equal(t, constants.GET_INFO_SCOPE, scope)
}

func TestRequestMethodsToScopes_GetInfo(t *testing.T) {
	scopes, err := RequestMethodsToScopes([]string{models.GET_INFO_METHOD})
	assert.NoError(t, err)
	assert.Equal(t, []string{constants.GET_INFO_SCOPE}, scopes)
}

func TestRequestMethodToScope_CreateConnection(t *testing.T) {
	scope, err := RequestMethodToScope(models.CREATE_CONNECTION_METHOD)
	assert.NoError(t, err)
	assert.Equal(t, constants.SUPERUSER_SCOPE, scope)
}
func TestScopeToRequestMethods_Superuser(t *testing.T) {
	methods := scopeToRequestMethods(constants.SUPERUSER_SCOPE)
	assert.Equal(t, []string{models.CREATE_CONNECTION_METHOD}, methods)
}

func TestGetPermittedMethods_AlwaysGranted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	permissionsSvc := NewPermissionsService(svc.DB, svc.EventPublisher)
	result := permissionsSvc.GetPermittedMethods(app, svc.LNClient)
	assert.Equal(t, GetAlwaysGrantedMethods(), result)
}

func TestGetPermittedMethods_PayInvoiceScopeGivesAllPaymentMethods(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.PAY_INVOICE_SCOPE,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	permissionsSvc := NewPermissionsService(svc.DB, svc.EventPublisher)
	result := permissionsSvc.GetPermittedMethods(app, svc.LNClient)
	assert.Contains(t, result, models.PAY_INVOICE_METHOD)
	assert.Contains(t, result, models.PAY_KEYSEND_METHOD)
	assert.Contains(t, result, models.MULTI_PAY_INVOICE_METHOD)
	assert.Contains(t, result, models.MULTI_PAY_KEYSEND_METHOD)
}

// Cash Hub scope: bidirectional mapping. claim_cash_wallet no longer exists —
// mint_cash is the only method this scope grants (replaced entirely
// by the new shared-wallet + cash_redeem model).
func TestScopeToRequestMethods_CashHub(t *testing.T) {
	methods := scopeToRequestMethods(constants.CASH_HUB_SCOPE)
	assert.Equal(t, []string{constants.NIP47MethodMintCash}, methods)
}

func TestRequestMethodToScope_CashHub(t *testing.T) {
	scope, err := RequestMethodToScope(constants.NIP47MethodMintCash)
	require.NoError(t, err)
	assert.Equal(t, constants.CASH_HUB_SCOPE, scope)
}

// Cash Claim Funds scope: bidirectional mapping. Granted on cash_wallet
// children instead of pay_invoice — covers both cash_redeem (the payout) and
// list_recipients (the read-only roster), since anyone allowed to attempt a
// claim may reasonably see the roster first.
func TestScopeToRequestMethods_CashClaimFunds(t *testing.T) {
	methods := scopeToRequestMethods(constants.CASH_REDEEM_SCOPE)
	assert.ElementsMatch(t, []string{constants.NIP47MethodCashRedeem, constants.NIP47MethodListRecipients}, methods)
}

func TestRequestMethodToScope_ClaimFunds(t *testing.T) {
	scope, err := RequestMethodToScope(constants.NIP47MethodCashRedeem)
	require.NoError(t, err)
	assert.Equal(t, constants.CASH_REDEEM_SCOPE, scope)
}

func TestRequestMethodToScope_ListRecipients(t *testing.T) {
	scope, err := RequestMethodToScope(constants.NIP47MethodListRecipients)
	require.NoError(t, err)
	assert.Equal(t, constants.CASH_REDEEM_SCOPE, scope)
}

func TestAllScopes_IncludesCashClaimFunds(t *testing.T) {
	assert.Contains(t, AllScopes(), constants.CASH_REDEEM_SCOPE)
}

// GetPermittedMethods must include cash_redeem/list_recipients for a
// cash_wallet regardless of what the (mock) LN client's own
// GetSupportedNIP47Methods() advertises — these are app-level abstractions
// over pay_invoice, not real LN-backend methods, mirroring how
// mint_cash/create_circle_wallet are already bypassed here.
func TestGetPermittedMethods_CashClaimFundsScope(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)

	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.CASH_REDEEM_SCOPE,
	}).Error)

	permissionsSvc := NewPermissionsService(svc.DB, svc.EventPublisher)
	result := permissionsSvc.GetPermittedMethods(app, svc.LNClient)
	assert.Contains(t, result, constants.NIP47MethodCashRedeem)
	assert.Contains(t, result, constants.NIP47MethodListRecipients)
	assert.NotContains(t, result, models.PAY_INVOICE_METHOD)
}

func TestGetPermittedMethods_CashHubScope(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)

	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.CASH_HUB_SCOPE,
	}).Error)

	permissionsSvc := NewPermissionsService(svc.DB, svc.EventPublisher)
	result := permissionsSvc.GetPermittedMethods(app, svc.LNClient)
	assert.Contains(t, result, constants.NIP47MethodMintCash)
	assert.NotContains(t, result, constants.NIP47MethodCreateCircleWallet)
}

// Circle Wallet scope: bidirectional mapping
func TestScopeToRequestMethods_CircleWallet(t *testing.T) {
	methods := scopeToRequestMethods(constants.CIRCLE_WALLET_SCOPE)
	assert.Equal(t, []string{constants.NIP47MethodCreateCircleWallet}, methods)
}

func TestRequestMethodToScope_CircleWallet(t *testing.T) {
	scope, err := RequestMethodToScope(constants.NIP47MethodCreateCircleWallet)
	require.NoError(t, err)
	assert.Equal(t, constants.CIRCLE_WALLET_SCOPE, scope)
}

func TestGetPermittedMethods_CircleWalletScope(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)

	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.CIRCLE_WALLET_SCOPE,
	}).Error)

	permissionsSvc := NewPermissionsService(svc.DB, svc.EventPublisher)
	result := permissionsSvc.GetPermittedMethods(app, svc.LNClient)
	assert.Contains(t, result, constants.NIP47MethodCreateCircleWallet)
	assert.NotContains(t, result, constants.NIP47MethodMintCash)
}
