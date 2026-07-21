package api

// GetApp (and ListApps) used to resolve MaxAmountLoki/BudgetRenewal/BudgetUsage
// exclusively from the pay_invoice-scope AppPermission row. A circle_hub is
// deliberately never granted pay_invoice (it only issues circle_wallet
// children — see nip47/controllers/create_circle_wallet_controller.go), so its
// own admin-set budget — stored on its circle_wallet-scope permission row —
// was silently dropped from every API response, even though it was correctly
// persisted and enforced. These tests lock in the fix: fall back to any
// permission row when no pay_invoice row exists.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func newTestAPIForGetApp(svc *tests.TestService) *api {
	return &api{db: svc.DB, appsSvc: svc.AppsService, keys: svc.Keys}
}

func TestGetApp_CircleHub_SurfacesOwnBudget(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"Circle Hub", "", 500, constants.BUDGET_RENEWAL_MONTHLY, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "Circle Hub", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 1_000_000},
	)
	require.NoError(t, err)

	theAPI := newTestAPIForGetApp(svc)
	result := theAPI.GetApp(context.Background(), provider)

	assert.Equal(t, uint64(500), result.MaxAmountLoki,
		"a circle_hub's own budget (set at creation, no pay_invoice scope involved) must round-trip through GetApp")
	assert.Equal(t, constants.BUDGET_RENEWAL_MONTHLY, result.BudgetRenewal)
}

func TestGetApp_CircleHub_BudgetUsageReflectsLiveCommitment(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"Circle Hub", "", 1000, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "Circle Hub", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 1_000_000},
	)
	require.NoError(t, err)

	// A live child committing 300 loki (300_000 mloki) — a circle_hub never
	// spends directly itself, so its own outgoing-transaction sum (what
	// GetBudgetUsageSat would compute) stays 0 regardless.
	future := time.Now().Add(time.Hour)
	_, _, err = svc.AppsService.CreateApp("existing-child", "", 300, "never", &future,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &provider.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)

	theAPI := newTestAPIForGetApp(svc)
	result := theAPI.GetApp(context.Background(), provider)

	assert.Equal(t, uint64(300), result.BudgetUsage,
		"a circle_hub's 'used' budget must reflect live commitment to children, not its own (always empty) outgoing spend")
}

func TestGetApp_StandardPayInvoiceApp_StillUsesPayInvoiceRow(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := svc.AppsService.CreateApp(
		"standard", "", 250, constants.BUDGET_RENEWAL_WEEKLY, nil,
		[]string{constants.PAY_INVOICE_SCOPE, constants.GET_INFO_SCOPE}, db.AppKindStandard, nil, "", nil,
	)
	require.NoError(t, err)

	theAPI := newTestAPIForGetApp(svc)
	result := theAPI.GetApp(context.Background(), app)

	assert.Equal(t, uint64(250), result.MaxAmountLoki,
		"an app with a real pay_invoice permission must keep using that row, unaffected by the circle_hub fallback")
	assert.Equal(t, constants.BUDGET_RENEWAL_WEEKLY, result.BudgetRenewal)
}
