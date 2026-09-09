package tests

import (
	"sync"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAppsService(svc *tests.TestService) apps.AppsService {
	return apps.NewAppsService(svc.DB, svc.EventPublisher, svc.Keys, svc.Cfg)
}

func TestHandleCreateApp_NilScopes(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, secretKey, err := newAppsService(svc).CreateApp("Test", "", 0, "monthly", nil, nil, "", nil, "", nil)

	assert.Nil(t, app)
	assert.Equal(t, "", secretKey)
	require.Error(t, err)
	assert.Equal(t, "no scopes provided", err.Error())
}

func TestHandleCreateApp_EmptyScopes(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, secretKey, err := newAppsService(svc).CreateApp("Test", "", 0, "monthly", nil, []string{}, "", nil, "", nil)

	assert.Nil(t, app)
	assert.Equal(t, "", secretKey)
	require.Error(t, err)
	assert.Equal(t, "no scopes provided", err.Error())
}

func TestCreateApp_KindPersisted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := newAppsService(svc).CreateApp(
		"hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE},
		db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)
	assert.Equal(t, db.AppKindIsolated, app.Kind)

	var loaded db.App
	require.NoError(t, svc.DB.First(&loaded, app.ID).Error)
	assert.Equal(t, db.AppKindIsolated, loaded.Kind)
}

func TestCreateApp_ParentAppID_SetForSubWallets(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := newAppsService(svc).CreateApp(
		"hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE},
		db.AppKindCashHub, nil, "", nil,
	)
	require.NoError(t, err)

	exp := time.Now().Add(time.Hour)
	child, _, err := newAppsService(svc).CreateApp(
		"cash", "", 0, "never", &exp,
		[]string{constants.PAY_INVOICE_SCOPE},
		db.AppKindCashWallet, &parent.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, child.ParentAppID)
	assert.Equal(t, parent.ID, *child.ParentAppID)
	assert.Equal(t, db.ParentKindCash, child.ParentKind)
}

func TestCreateCashHub_ConfigPersisted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	appSvc := newAppsService(svc)
	app, _, err := appSvc.CreateCashHub(
		"hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE, constants.CASH_HUB_SCOPE},
		nil,
		db.CashHubConfig{PerWalletMaxMloki: 500_000, MaxExpSecs: 3600},
	)
	require.NoError(t, err)
	assert.Equal(t, db.AppKindCashHub, app.Kind)

	cfg, err := appSvc.GetCashHubConfig(app.ID)
	require.NoError(t, err)
	assert.Equal(t, 500_000, cfg.PerWalletMaxMloki)
	assert.Equal(t, 3600, cfg.MaxExpSecs)
}

// TestDeleteApp_CashHubWithChildren_Refused guards against orphaning a
// cash_wallet child: deleting its cash_hub parent out from under it leaves the
// child's parent_app_id pointing at a nonexistent app row, so the periodic
// cleanup ticker (service.ReclaimAndDeleteSubWallet) fails a FOREIGN KEY
// constraint every time it tries to credit the reclaimed balance back to
// that parent — forever, since the child never gets deleted. This mirrors
// the existing circle_hub guard below.
func TestDeleteApp_CashHubWithChildren_Refused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	appSvc := newAppsService(svc)
	hub, _, err := appSvc.CreateCashHub(
		"hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE, constants.CASH_HUB_SCOPE},
		nil,
		db.CashHubConfig{PerWalletMaxMloki: 500_000, MaxExpSecs: 3600},
	)
	require.NoError(t, err)

	exp := time.Now().Add(time.Hour)
	_, _, err = appSvc.CreateApp(
		"cash", "", 0, "never", &exp,
		[]string{constants.PAY_INVOICE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)

	err = appSvc.DeleteApp(hub)
	require.Error(t, err, "cash_hub with a live cash_wallet child must not be deletable")
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	var stillThere db.App
	assert.NoError(t, svc.DB.First(&stillThere, hub.ID).Error, "hub must still exist after refused delete")
}

// TestDeleteApp_CashHubWithoutChildren_Succeeds is the counterpart to the
// guard above: a cash_hub with no outstanding children must still be
// deletable normally.
func TestDeleteApp_CashHubWithoutChildren_Succeeds(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	appSvc := newAppsService(svc)
	hub, _, err := appSvc.CreateCashHub(
		"hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE, constants.CASH_HUB_SCOPE},
		nil,
		db.CashHubConfig{PerWalletMaxMloki: 500_000, MaxExpSecs: 3600},
	)
	require.NoError(t, err)

	require.NoError(t, appSvc.DeleteApp(hub))

	var gone db.App
	assert.Error(t, svc.DB.First(&gone, hub.ID).Error, "childless hub must be deleted")
}

func TestIsIsolated_ReturnsTrueForSubWalletKinds(t *testing.T) {
	cases := []struct {
		kind     string
		expected bool
	}{
		{db.AppKindIsolated, true},
		{db.AppKindCashHub, true},
		{db.AppKindCashWallet, true},
		{db.AppKindCircleHub, true},
		{db.AppKindCircleWallet, true},
		{"", false},
	}
	for _, tc := range cases {
		app := db.App{Kind: tc.kind}
		assert.Equal(t, tc.expected, app.IsIsolated(), "kind=%q", tc.kind)
	}
}

// E9: ClaimCashSlice is race-safe — concurrent calls for the same
// identity must result in exactly one success and the rest failing with
// ErrNotFound ("this slice has already been redeemed") through the same
// atomic guard.
func TestClaimCashSlice_ConcurrentRace(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)

	future := time.Now().Add(time.Hour)
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-race", "", 0, constants.BUDGET_RENEWAL_NEVER, &future,
		[]string{constants.CASH_REDEEM_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)

	identityValue := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: identityValue, AmountMloki: 100_000},
	}))

	const goroutines = 5
	type result struct {
		amount int64
		err    error
	}
	results := make(chan result, goroutines)
	ready := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			amount, err := svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, identityValue)
			results <- result{amount, err}
		}()
	}
	close(ready)
	wg.Wait()
	close(results)

	var successes, failures int
	for r := range results {
		if r.err == nil {
			successes++
			assert.Equal(t, int64(100_000), r.amount)
		} else {
			failures++
			assert.ErrorIs(t, r.err, constants.ErrNotFound)
		}
	}
	assert.Equal(t, 1, successes, "exactly one goroutine must claim successfully")
	assert.Equal(t, goroutines-1, failures, "every other goroutine must fail the atomic claim")
}
