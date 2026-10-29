package ledger

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

func twoApps(t *testing.T, svc *tests.TestService) (from, to *db.App) {
	t.Helper()
	from = tests.CreateCashHub(t, svc, 100_000_000, 3600)
	to, _, err := svc.AppsService.CreateApp("to", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindCashWallet, &from.ID, db.ParentKindCash, nil)
	require.NoError(t, err)
	return from, to
}

// U-4.9: the transfer is two SETTLED rows with no Lightning invoice, the balance
// moves inside the transaction, and an overdraft is refused with nothing written.
func TestInternalTransferTx_MovesBalanceWithoutAnInvoice(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	from, to := twoApps(t, svc)
	tests.FundApp(svc, from.ID, 10_000, "fund")

	require.NoError(t, svc.DB.Transaction(func(tx *gorm.DB) error {
		if err := InternalTransferTx(tx, from.ID, to.ID, 4_000, "cash mint"); err != nil {
			return err
		}
		assert.Equal(t, int64(6_000), queries.GetIsolatedBalance(tx, from.ID))
		assert.Equal(t, int64(4_000), queries.GetIsolatedBalance(tx, to.ID))
		return nil
	}))

	var rows []db.Transaction
	require.NoError(t, svc.DB.Where("app_id IN ? AND payment_hash <> ?", []uint{from.ID, to.ID}, "fund").Find(&rows).Error)
	require.Len(t, rows, 2)
	for _, r := range rows {
		assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, r.State)
		assert.Empty(t, r.PaymentRequest, "no Lightning invoice exists for an internal transfer")
		assert.Zero(t, r.FeeMloki)
		assert.Zero(t, r.FeeReserveMloki)
		assert.Equal(t, rows[0].PaymentHash, r.PaymentHash, "both legs share one synthetic hash")
	}
	assert.NotEqual(t, rows[0].Type, rows[1].Type)
}

func TestInternalTransferTx_RefusesOverdraft(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	from, to := twoApps(t, svc)
	tests.FundApp(svc, from.ID, 1_000, "fund")

	err = svc.DB.Transaction(func(tx *gorm.DB) error {
		return InternalTransferTx(tx, from.ID, to.ID, 1_001, "cash mint")
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, transactions.NewInsufficientBalanceError()))
	assert.Equal(t, int64(1_000), queries.GetIsolatedBalance(svc.DB, from.ID))
	assert.Equal(t, int64(0), queries.GetIsolatedBalance(svc.DB, to.ID))
}

func TestInternalTransferTx_RollsBackWithItsTransaction(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	from, to := twoApps(t, svc)
	tests.FundApp(svc, from.ID, 1_000, "fund")

	boom := errors.New("later step failed")
	err = svc.DB.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, InternalTransferTx(tx, from.ID, to.ID, 500, "cash mint"))
		return boom
	})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, int64(1_000), queries.GetIsolatedBalance(svc.DB, from.ID))
	assert.Equal(t, int64(0), queries.GetIsolatedBalance(svc.DB, to.ID))
}

func TestInternalTransferTx_RefusesZeroAndSelf(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	from, to := twoApps(t, svc)
	tests.FundApp(svc, from.ID, 1_000, "fund")
	require.Error(t, svc.DB.Transaction(func(tx *gorm.DB) error { return InternalTransferTx(tx, from.ID, to.ID, 0, "x") }))
	require.Error(t, svc.DB.Transaction(func(tx *gorm.DB) error { return InternalTransferTx(tx, from.ID, from.ID, 1, "x") }))
}
