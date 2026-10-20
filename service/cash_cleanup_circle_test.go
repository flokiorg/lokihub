package service

// The expiry sweep on circle wallets: a funded wallet's balance goes back to its
// hub, and a wallet with a payment still in flight, either way, is left for the
// next tick.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

func expiredCircleChild(t *testing.T, svc *tests.TestService) (hub, child *db.App) {
	t.Helper()
	hub, _, err := svc.AppsService.CreateApp("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)
	return hub, createSubWallet(t, svc, db.AppKindCircleWallet, hub.ID, db.ParentKindCircle, makeExpiredTime())
}

func TestRunCashCleanup_CircleChild_Funded_ReclaimedToHub(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	svc.LNClient.(*tests.MockLn).Pubkey = selfPaymentPubkey

	hub, child := expiredCircleChild(t, svc)
	tests.FundApp(svc, child.ID, 200_000, "circle-cleanup-fund")

	runCashCleanup(ctx, svc.DB, transactions.NewTransactionsService(svc.DB, svc.EventPublisher), svc.LNClient, svc.EventPublisher)

	var found db.App
	assert.Error(t, svc.DB.First(&found, child.ID).Error, "the expired circle wallet is deleted")
	// The mock LN client pays a fixed invoice whatever amount is asked, so only
	// the direction of the transfer is checked (as for cash wallets).
	assert.Greater(t, queries.GetIsolatedBalance(svc.DB, hub.ID), int64(0), "its balance goes back to the hub")
}

// Deleting the wallet would cascade-delete the outgoing row, so the payment's
// settlement (and the hub's fee credit) would have nothing to update.
func TestRunCashCleanup_CircleChild_PendingOutgoing_DeferredNotDeleted(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, child := expiredCircleChild(t, svc)
	tests.FundApp(svc, child.ID, 200_000, "circle-cleanup-fund-out")
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:       &child.ID,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		State:       constants.TRANSACTION_STATE_PENDING,
		AmountMloki: 50_000,
		PaymentHash: "pending-outgoing-hash",
	}).Error)

	runCashCleanup(ctx, svc.DB, transactions.NewTransactionsService(svc.DB, svc.EventPublisher), svc.LNClient, svc.EventPublisher)

	var found db.App
	require.NoError(t, svc.DB.First(&found, child.ID).Error, "a wallet with a payment in flight must not be deleted")
	assert.False(t, found.CleanupInProgress, "the next tick retries")
	var stillPending db.Transaction
	require.NoError(t, svc.DB.Where("payment_hash = ?", "pending-outgoing-hash").First(&stillPending).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_PENDING, stillPending.State)
}
