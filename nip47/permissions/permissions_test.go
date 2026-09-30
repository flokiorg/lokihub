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
// cash_status (the read-only roster), since anyone allowed to attempt a claim
// may reasonably see the roster first.
//
// One name for the roster now: the list_recipients alias is gone, so get_info
// advertises cash_status alone.
func TestScopeToRequestMethods_CashClaimFunds(t *testing.T) {
	methods := scopeToRequestMethods(constants.CASH_REDEEM_SCOPE)
	assert.ElementsMatch(t, []string{
		constants.NIP47MethodCashRedeem,
		constants.NIP47MethodCashStatus,
	}, methods)
}

func TestRequestMethodToScope_ClaimFunds(t *testing.T) {
	scope, err := RequestMethodToScope(constants.NIP47MethodCashRedeem)
	require.NoError(t, err)
	assert.Equal(t, constants.CASH_REDEEM_SCOPE, scope)
}

func TestRequestMethodToScope_CashStatus(t *testing.T) {
	scope, err := RequestMethodToScope(constants.NIP47MethodCashStatus)
	require.NoError(t, err)
	assert.Equal(t, constants.CASH_REDEEM_SCOPE, scope)
}

func TestAllScopes_IncludesCashClaimFunds(t *testing.T) {
	assert.Contains(t, AllScopes(), constants.CASH_REDEEM_SCOPE)
}

// GetPermittedMethods MUST NOT advertise the bill methods, and the scope that grants
// them MUST still grant them.
//
// Inverted from an earlier version that asserted cash_redeem/cash_status WERE advertised.
// That was right while they were served on kind 23194; it is wrong now. get_info describes
// one kind-23194 connection, and these four are served over the private transport only
// (NIP-CASH §It is the ONLY transport for the bill methods) — so advertising them promised
// a caller something that transport refuses, and every client following get_info was sent
// down a path that cannot work.
//
// Both halves are asserted together on purpose, because the tempting fix — dropping the
// scope — would have refused every real call while making this test pass. Advertising and
// authorizing are different questions, and only the first changed.
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

	// Not advertised: this connection cannot serve them.
	assert.NotContains(t, result, constants.NIP47MethodCashRedeem,
		"a bill must not advertise a method its own transport refuses")
	assert.NotContains(t, result, constants.NIP47MethodCashStatus,
		"a bill must not advertise a method its own transport refuses")
	// And never granted by this scope anyway.
	assert.NotContains(t, result, models.PAY_INVOICE_METHOD)

	// Still GRANTED, which is what the private transport checks per item. Read from the
	// scope mapping rather than from the advertised list, since those are now different
	// questions.
	granted := scopeToRequestMethods(constants.CASH_REDEEM_SCOPE)
	assert.Contains(t, granted, constants.NIP47MethodCashRedeem,
		"the scope must still grant cash_redeem, or every real call is refused")
	assert.Contains(t, granted, constants.NIP47MethodCashStatus)

	// And DISCOVERABLE: removing them from `methods` left no wire-level signal that they
	// exist, so they are reported separately. This is the third leg — not advertised as
	// callable here, still granted, and still findable.
	private := permissionsSvc.GetPrivateMethods(app)
	assert.Contains(t, private, constants.NIP47MethodCashRedeem)
	assert.Contains(t, private, constants.NIP47MethodCashStatus)
	for _, m := range private {
		assert.NotContains(t, result, m,
			"a method cannot be both advertised as callable here and reported as private")
	}
}

// TestGetPrivateMethods_ReflectsGrantsNotAFixedList: the field is derived from what this
// app was actually granted, never a hardcoded set.
//
// The distinction matters because a fixed list would tell a client to attempt a method this
// particular bill was never given — which the hub would then refuse, putting the client back
// in the position the field exists to remove.
func TestGetPrivateMethods_ReflectsGrantsNotAFixedList(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	permissionsSvc := NewPermissionsService(svc.DB, svc.EventPublisher)

	// No cash scopes at all: nothing private to report.
	assert.Empty(t, permissionsSvc.GetPrivateMethods(app),
		"an app with no cash scopes must report no private methods")

	// One scope granted: only its own methods appear, not the whole bill-method set.
	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId: app.ID, App: *app, Scope: constants.CASH_TRANSFER_SCOPE,
	}).Error)
	private := permissionsSvc.GetPrivateMethods(app)
	assert.Contains(t, private, constants.NIP47MethodCashTransfer)
	assert.NotContains(t, private, constants.NIP47MethodCashConsolidate,
		"a scope this app was never granted must not be reported as available")
}

// TestGetPermittedMethods_BillStillAdvertisesWhatItCanServe is the other side of the
// filter: dropping the private-only four must not take the rest with them.
//
// A bill still answers get_balance and get_info on kind 23194, and a client needs to know
// that. A filter that removed too much would leave a bill looking like it supports nothing.
func TestGetPermittedMethods_BillStillAdvertisesWhatItCanServe(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId: app.ID, App: *app, Scope: constants.GET_BALANCE_SCOPE,
	}).Error)

	result := NewPermissionsService(svc.DB, svc.EventPublisher).GetPermittedMethods(app, svc.LNClient)
	assert.Contains(t, result, models.GET_BALANCE_METHOD,
		"the private-only filter must not drop methods this transport does serve")
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
