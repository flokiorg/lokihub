package queries

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestGetIsolatedBalancesByAppIDs_EmptyInput(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	balances, err := GetIsolatedBalancesByAppIDs(svc.DB, nil)
	require.NoError(t, err)
	assert.Empty(t, balances)
}

func TestGetIsolatedBalancesByAppIDs_MixedAndMissing(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("parent", "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)

	funded, _, err := svc.AppsService.CreateApp("funded", "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)
	tests.FundApp(svc, funded.ID, 42_000, "batched-balance-test-1")

	// empty has transaction rows that net to a zero balance (unlike noTransactions
	// below, which has none at all) — it must still appear in the result map,
	// with balance 0, since the GROUP BY produces a row for any app_id with at
	// least one transaction.
	empty, _, err := svc.AppsService.CreateApp("empty", "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)
	tests.FundApp(svc, empty.ID, 10_000, "batched-balance-test-2")
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:       &empty.ID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		AmountMloki: 10_000,
		PaymentHash: "batched-balance-test-3",
	}).Error)

	// noTransactions is a valid app ID with zero transaction rows at all — it
	// must be treated as balance 0 by the caller even though it has no row in
	// the aggregate result.
	noTransactions, _, err := svc.AppsService.CreateApp("no-tx", "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)

	balances, err := GetIsolatedBalancesByAppIDs(svc.DB, []uint{funded.ID, empty.ID, noTransactions.ID})
	require.NoError(t, err)

	assert.Equal(t, int64(42_000), balances[funded.ID])
	emptyBalance, emptyOk := balances[empty.ID]
	assert.True(t, emptyOk, "an app with transactions netting to zero must still appear in the map")
	assert.Equal(t, int64(0), emptyBalance)
	_, noTxOk := balances[noTransactions.ID]
	assert.False(t, noTxOk, "an app with no transactions must be absent from the map, not zero-valued")
}

func TestGetIsolatedBalancesByAppIDs_FeeSkimIncluded(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	app.Kind = db.AppKindCircleWallet
	svc.DB.Save(&app)

	tests.FundApp(svc, app.ID, 50_000, "batched-balance-feeskim-1")
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:        &app.ID,
		State:        constants.TRANSACTION_STATE_SETTLED,
		Type:         constants.TRANSACTION_TYPE_OUTGOING,
		AmountMloki:  20_000,
		FeeSkimMloki: 200,
		PaymentHash:  "batched-balance-feeskim-2",
	}).Error)

	balances, err := GetIsolatedBalancesByAppIDs(svc.DB, []uint{app.ID})
	require.NoError(t, err)
	assert.Equal(t, int64(50_000-20_000-200), balances[app.ID])
}
