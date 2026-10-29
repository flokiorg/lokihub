package service

// The cash expiry sweep never touches a Circle Wallet (U-16.10): a member's
// balance is never swept, and an expired wallet is the lifecycle job's.

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

func TestRunCashCleanup_CircleChild_NeverSwept(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	svc.LNClient.(*tests.MockLn).Pubkey = selfPaymentPubkey

	hub, funded := expiredCircleChild(t, svc)
	tests.FundApp(svc, funded.ID, 200_000, "circle-cleanup-fund")
	empty := createSubWallet(t, svc, db.AppKindCircleWallet, hub.ID, db.ParentKindCircle, makeExpiredTime())

	runCashCleanup(ctx, svc.DB, transactions.NewTransactionsService(svc.DB, svc.EventPublisher), svc.LNClient, svc.EventPublisher)

	for _, id := range []uint{funded.ID, empty.ID} {
		var found db.App
		require.NoError(t, svc.DB.First(&found, id).Error, "an expired circle wallet stays for its lifecycle")
		assert.False(t, found.CleanupInProgress)
	}
	assert.Equal(t, int64(200_000), queries.GetIsolatedBalance(svc.DB, funded.ID))
	assert.Zero(t, queries.GetIsolatedBalance(svc.DB, hub.ID), "nothing reached the hub")

	err = ReclaimAndDeleteSubWallet(ctx, svc.DB, transactions.NewTransactionsService(svc.DB, svc.EventPublisher), svc.LNClient, *funded, SubWalletDeletion{})
	assert.ErrorIs(t, err, constants.ErrInvalidParams, "the reclaim path refuses a circle wallet")
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
