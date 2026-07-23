package api

// F5 — UpdateApp has no immutability guard for jit_hub, jit_wallet, or
// circle_child apps, allowing scopes and budget to be changed after creation.
//
// F6 — UpdateApp blocks circle_hub apps entirely (returns ErrKindImmutable)
// even for name-only changes that do not touch scopes or budget.
//
// Both tests assert the CORRECT behaviour and FAIL today against the current code.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/tests/mocks"
)

// newTestAPIWithEventPub wires a real DB and a minimal mock service that
// returns the test EventPublisher.  UpdateApp calls svc.GetEventPublisher() on
// success, so any test whose happy path reaches that call would panic without it.
func newTestAPIWithEventPub(t *testing.T, svc *tests.TestService) *api {
	t.Helper()
	mockSvc := mocks.NewMockService(t)
	mockSvc.On("GetEventPublisher").Return(svc.EventPublisher).Maybe()
	return &api{db: svc.DB, appsSvc: svc.AppsService, svc: mockSvc}
}

// TestUpdateApp_JITWallet_ScopeChange_IsRejected — F5
// A jit_wallet app must not allow scope changes via UpdateApp.
func TestUpdateApp_JITWallet_ScopeChange_IsRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := &db.App{Name: "jit-wallet", Kind: db.AppKindJITWallet}
	require.NoError(t, svc.DB.Create(app).Error)
	perm := &db.AppPermission{AppId: app.ID, App: *app, Scope: constants.PAY_INVOICE_SCOPE}
	require.NoError(t, svc.DB.Create(&perm).Error)

	theAPI := newTestAPIWithEventPub(t, svc)
	err = theAPI.UpdateApp(app, &UpdateAppRequest{
		Scopes: []string{constants.PAY_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE, constants.SIGN_MESSAGE_SCOPE},
	})

	// Correct behaviour: privileged-kind apps must be immutable.
	assert.Error(t, err, "scope change on a jit_wallet must be rejected")
}

// TestUpdateApp_JITHub_ScopeChange_IsRejected — F5 (hub variant)
func TestUpdateApp_JITHub_ScopeChange_IsRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := &db.App{Name: "jit-hub", Kind: db.AppKindJITHub}
	require.NoError(t, svc.DB.Create(app).Error)
	perm := &db.AppPermission{AppId: app.ID, App: *app, Scope: constants.GET_BALANCE_SCOPE}
	require.NoError(t, svc.DB.Create(&perm).Error)

	theAPI := newTestAPIWithEventPub(t, svc)
	err = theAPI.UpdateApp(app, &UpdateAppRequest{
		Scopes: []string{constants.GET_BALANCE_SCOPE, constants.SIGN_MESSAGE_SCOPE},
	})

	assert.Error(t, err, "scope change on a jit_hub must be rejected")
}

// TestUpdateApp_CircleHub_NameOnly_Succeeds — F6
// A name-only change on a circle_hub app must succeed; today the code
// returns ErrKindImmutable for ALL updates (including name-only), which is too
// broad — name changes do not affect wallet behaviour.
func TestUpdateApp_CircleHub_NameOnly_Succeeds(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := &db.App{Name: "circle-admin", Kind: db.AppKindCircleHub}
	require.NoError(t, svc.DB.Create(app).Error)

	theAPI := newTestAPIWithEventPub(t, svc)
	newName := "circle-admin-renamed"
	err = theAPI.UpdateApp(app, &UpdateAppRequest{Name: &newName})

	// Correct behaviour: name-only update must succeed for circle_hub.
	assert.NoError(t, err,
		"name-only UpdateApp must succeed for circle_hub; ErrKindImmutable must only block scope/budget changes")

	// Verify the name was actually persisted.
	var updated db.App
	require.NoError(t, svc.DB.First(&updated, app.ID).Error)
	assert.Equal(t, newName, updated.Name)
}

// TestUpdateApp_CircleHub_ScopeChange_IsRejected verifies that the
// immutability guard still blocks scope changes (i.e. fixing F6 must not
// accidentally remove the guard entirely).
func TestUpdateApp_CircleHub_ScopeChange_IsRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := &db.App{Name: "circle-admin", Kind: db.AppKindCircleHub}
	require.NoError(t, svc.DB.Create(app).Error)
	perm := &db.AppPermission{AppId: app.ID, App: *app, Scope: constants.GET_BALANCE_SCOPE}
	require.NoError(t, svc.DB.Create(&perm).Error)

	theAPI := newTestAPIWithEventPub(t, svc)
	err = theAPI.UpdateApp(app, &UpdateAppRequest{
		Scopes: []string{constants.GET_BALANCE_SCOPE, constants.SIGN_MESSAGE_SCOPE},
	})

	assert.Error(t, err, "scope change on circle_hub must still be rejected after F6 fix")
}

// TestUpdateApp_CircleHub_BudgetChange_Succeeds — a circle hub's own
// budget and expiry must be user-configurable like a regular app, unlike the
// wallets it issues.
func TestUpdateApp_CircleHub_BudgetChange_Succeeds(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := &db.App{Name: "circle-admin", Kind: db.AppKindCircleHub}
	require.NoError(t, svc.DB.Create(app).Error)
	perm := &db.AppPermission{AppId: app.ID, App: *app, Scope: constants.PAY_INVOICE_SCOPE}
	require.NoError(t, svc.DB.Create(&perm).Error)

	theAPI := newTestAPIWithEventPub(t, svc)
	maxAmount := uint64(100000)
	budgetRenewal := constants.BUDGET_RENEWAL_MONTHLY
	err = theAPI.UpdateApp(app, &UpdateAppRequest{
		MaxAmountLoki: &maxAmount,
		BudgetRenewal: &budgetRenewal,
	})
	require.NoError(t, err, "budget change on circle_hub must be allowed")

	var updated db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ?", app.ID).First(&updated).Error)
	assert.Equal(t, int(maxAmount), updated.MaxAmountLoki) //nolint:gosec // maxAmount is a small hardcoded test literal
	assert.Equal(t, budgetRenewal, updated.BudgetRenewal)
}

// TestUpdateApp_JITHub_BudgetChange_Succeeds — like a circle hub, a JIT hub's
// own budget and expiry must be user-configurable, unlike the jit_wallet
// children it issues.
func TestUpdateApp_JITHub_BudgetChange_Succeeds(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := &db.App{Name: "jit-hub", Kind: db.AppKindJITHub}
	require.NoError(t, svc.DB.Create(app).Error)
	perm := &db.AppPermission{AppId: app.ID, App: *app, Scope: constants.PAY_INVOICE_SCOPE}
	require.NoError(t, svc.DB.Create(&perm).Error)

	theAPI := newTestAPIWithEventPub(t, svc)
	maxAmount := uint64(100000)
	budgetRenewal := constants.BUDGET_RENEWAL_MONTHLY
	err = theAPI.UpdateApp(app, &UpdateAppRequest{
		MaxAmountLoki: &maxAmount,
		BudgetRenewal: &budgetRenewal,
	})
	require.NoError(t, err, "budget change on jit_hub must be allowed")

	var updated db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ?", app.ID).First(&updated).Error)
	assert.Equal(t, int(maxAmount), updated.MaxAmountLoki) //nolint:gosec // maxAmount is a small hardcoded test literal
	assert.Equal(t, budgetRenewal, updated.BudgetRenewal)
}

// TestUpdateApp_JITWallet_BudgetChange_IsRejected — jit_wallet children remain
// fully system-managed: their limits come from the create_jit_wallet flow.
func TestUpdateApp_JITWallet_BudgetChange_IsRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := &db.App{Name: "jit-wallet", Kind: db.AppKindJITWallet}
	require.NoError(t, svc.DB.Create(app).Error)
	perm := &db.AppPermission{AppId: app.ID, App: *app, Scope: constants.PAY_INVOICE_SCOPE}
	require.NoError(t, svc.DB.Create(&perm).Error)

	theAPI := newTestAPIWithEventPub(t, svc)
	maxAmount := uint64(100000)
	err = theAPI.UpdateApp(app, &UpdateAppRequest{MaxAmountLoki: &maxAmount})

	assert.Equal(t, constants.ErrKindImmutable, err, "budget change on jit_wallet must still be rejected")
}

// TestUpdateApp_CircleWallet_BudgetChange_IsRejected — unlike circle_hub,
// individual circle wallets remain fully system-managed: their budget/expiry
// come from the create_circle_wallet NIP-47 flow and must stay immutable here.
func TestUpdateApp_CircleWallet_BudgetChange_IsRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := &db.App{Name: "circle-wallet", Kind: db.AppKindCircleWallet}
	require.NoError(t, svc.DB.Create(app).Error)
	perm := &db.AppPermission{AppId: app.ID, App: *app, Scope: constants.PAY_INVOICE_SCOPE}
	require.NoError(t, svc.DB.Create(&perm).Error)

	theAPI := newTestAPIWithEventPub(t, svc)
	maxAmount := uint64(100000)
	err = theAPI.UpdateApp(app, &UpdateAppRequest{MaxAmountLoki: &maxAmount})

	assert.Equal(t, constants.ErrKindImmutable, err, "budget change on circle_wallet must still be rejected")
}

// TestUpdateApp_JITWalletCircleWallet_NameChange_IsRejected — JIT/circle
// wallet names are system-generated (apps.GenerateChildName: "<hub> ·
// <identity label> · <random>") and carry the identity used to resolve a
// Nostr profile name for display. A free-form rename would silently break
// that, so unlike circle_hub/jit_hub, these kinds must reject name changes too.
func TestUpdateApp_JITWalletCircleWallet_NameChange_IsRejected(t *testing.T) {
	for _, kind := range []string{db.AppKindJITWallet, db.AppKindCircleWallet} {
		t.Run(kind, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			const originalName = "hub · b9dbedf3 · 072f"
			app := &db.App{Name: originalName, Kind: kind}
			require.NoError(t, svc.DB.Create(app).Error)

			theAPI := newTestAPIWithEventPub(t, svc)
			newName := "renamed"
			err = theAPI.UpdateApp(app, &UpdateAppRequest{Name: &newName})

			assert.Equal(t, constants.ErrKindImmutable, err, "name change on %s must be rejected", kind)

			var reloaded db.App
			require.NoError(t, svc.DB.First(&reloaded, app.ID).Error)
			assert.Equal(t, originalName, reloaded.Name, "name must be untouched for %s", kind)
		})
	}
}
