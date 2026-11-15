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

// TestGetIsolatedBalancesByAppIDs_SpansChunks is the regression test for the
// driver's bind-variable ceiling.
//
// Every app ID used to bind as its own SQL variable, so a provider with more
// children than the ceiling (~32k on sqlite, ~65k on postgres) got "too many
// SQL variables" — an error, not a slow answer — from the Cash Hub dashboard,
// the Circle Hub stats and the children listing. The query now chunks, and this
// pins both halves of that: a list far past the old ceiling must succeed, and
// real balances must still be found wherever they fall across chunk boundaries.
//
// The list is padded with IDs that do not exist so the three real apps land in
// three different chunks without seeding thousands of rows.
func TestGetIsolatedBalancesByAppIDs_SpansChunks(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("parent", "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)

	newFunded := func(name string, amount uint64, hash string) uint {
		app, _, err := svc.AppsService.CreateApp(name, "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
		require.NoError(t, err)
		tests.FundApp(svc, app.ID, amount, hash)
		return app.ID
	}

	first := newFunded("first", 11_000, "chunk-span-1")
	middle := newFunded("middle", 22_000, "chunk-span-2")
	last := newFunded("last", 33_000, "chunk-span-3")

	// 50k IDs: comfortably past the old sqlite ceiling, and more than the
	// postgres one too.
	const total = 50_000
	appIDs := make([]uint, 0, total)
	place := map[int]uint{
		0:                      first,
		balanceChunkSize + 500: middle,
		total - 1:              last,
	}
	// Start padding well above any real app ID so no filler collides with one.
	filler := uint(1_000_000)
	for i := 0; i < total; i++ {
		if id, ok := place[i]; ok {
			appIDs = append(appIDs, id)
			continue
		}
		filler++
		appIDs = append(appIDs, filler)
	}

	balances, err := GetIsolatedBalancesByAppIDs(svc.DB, appIDs)
	require.NoError(t, err, "a list past the driver's bind-variable ceiling must succeed")

	assert.Equal(t, int64(11_000), balances[first], "first chunk")
	assert.Equal(t, int64(22_000), balances[middle], "a later chunk")
	assert.Equal(t, int64(33_000), balances[last], "the final, partial chunk")
	assert.Len(t, balances, 3, "IDs with no transactions must not appear")
}

// TestGetIsolatedBalancesByAppIDs_ExactChunkBoundary covers the off-by-one
// shape: a list whose length is exactly the chunk size must issue its single
// chunk and not a trailing empty one.
func TestGetIsolatedBalancesByAppIDs_ExactChunkBoundary(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("parent", "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)
	funded, _, err := svc.AppsService.CreateApp("funded", "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)
	tests.FundApp(svc, funded.ID, 7_000, "chunk-exact-1")

	appIDs := make([]uint, 0, balanceChunkSize)
	appIDs = append(appIDs, funded.ID)
	filler := uint(2_000_000)
	for len(appIDs) < balanceChunkSize {
		filler++
		appIDs = append(appIDs, filler)
	}
	require.Len(t, appIDs, balanceChunkSize)

	balances, err := GetIsolatedBalancesByAppIDs(svc.DB, appIDs)
	require.NoError(t, err)
	assert.Equal(t, int64(7_000), balances[funded.ID])
}
