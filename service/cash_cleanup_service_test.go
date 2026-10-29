package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// selfPaymentPubkey is the destination pubkey embedded in tests.MockInvoice.
// SendPaymentSync detects self-payment when lnClient.GetPubkey() == this value.
const selfPaymentPubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"

func makeExpiredTime() *time.Time {
	t := time.Now().Add(-time.Hour)
	return &t
}

func makeFutureTime() *time.Time {
	t := time.Now().Add(time.Hour)
	return &t
}

// createSubWallet creates a cash_wallet or circle_child with the given parent and expiry.
func createSubWallet(t *testing.T, svc *tests.TestService, kind string, parentID uint, parentKind string, expiresAt *time.Time) *db.App {
	t.Helper()
	parent := uint(parentID)
	child, _, err := svc.AppsService.CreateApp(
		"child",
		"",
		0,
		constants.BUDGET_RENEWAL_NEVER,
		expiresAt,
		[]string{constants.PAY_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		kind,
		&parent,
		parentKind,
		nil,
	)
	require.NoError(t, err)
	return child
}

func TestRunCashCleanup_NotExpired_AppNotCleaned(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCashHub, nil, "", nil)
	require.NoError(t, err)

	child := createSubWallet(t, svc, db.AppKindCashWallet, parent.ID, db.ParentKindCash, makeFutureTime())

	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	runCashCleanup(ctx, svc.DB, transactionsSvc, svc.LNClient, svc.EventPublisher)

	var found db.App
	err = svc.DB.First(&found, child.ID).Error
	assert.NoError(t, err, "non-expired app must not be deleted")
}

func TestRunCashCleanup_ZeroBalance_AppDeleted(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCashHub, nil, "", nil)
	require.NoError(t, err)

	child := createSubWallet(t, svc, db.AppKindCashWallet, parent.ID, db.ParentKindCash, makeExpiredTime())

	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	runCashCleanup(ctx, svc.DB, transactionsSvc, svc.LNClient, svc.EventPublisher)

	var found db.App
	err = svc.DB.First(&found, child.ID).Error
	assert.Error(t, err, "zero-balance expired app must be deleted")

	// Parent balance unchanged (no transfer happened).
	parentBalance := queries.GetIsolatedBalance(svc.DB, parent.ID)
	assert.Equal(t, int64(0), parentBalance)
}

func TestRunCashCleanup_CleanupInProgress_Skipped(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCashHub, nil, "", nil)
	require.NoError(t, err)

	child := createSubWallet(t, svc, db.AppKindCashWallet, parent.ID, db.ParentKindCash, makeExpiredTime())

	// Mark cleanup already in progress — simulates a concurrent cleanup run.
	svc.DB.Model(&db.App{}).Where("id = ?", child.ID).Update("cleanup_in_progress", true)

	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	runCashCleanup(ctx, svc.DB, transactionsSvc, svc.LNClient, svc.EventPublisher)

	// App must still exist because cleanup_in_progress = true excluded it from the query.
	var found db.App
	err = svc.DB.First(&found, child.ID).Error
	assert.NoError(t, err, "app with cleanup_in_progress=true must not be touched")
	assert.True(t, found.CleanupInProgress)
}

func TestRunCashCleanup_WithBalance_TransferAndDeleted(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Set pubkey so the mock LN client is recognised as the invoice destination
	// and the transfer is treated as a self-payment.
	svc.LNClient.(*tests.MockLn).Pubkey = selfPaymentPubkey

	parent, _, err := svc.AppsService.CreateApp("hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCashHub, nil, "", nil)
	require.NoError(t, err)

	child := createSubWallet(t, svc, db.AppKindCashWallet, parent.ID, db.ParentKindCash, makeExpiredTime())

	// MockInvoice encodes 123_000 mloki; fund well above that so validateCanPay passes.
	const fundedMloki = uint64(200_000)
	tests.FundApp(svc, child.ID, fundedMloki, "cleanup-test-hash")

	assert.Equal(t, int64(fundedMloki), queries.GetIsolatedBalance(svc.DB, child.ID))

	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	runCashCleanup(ctx, svc.DB, transactionsSvc, svc.LNClient, svc.EventPublisher)

	// Child must be deleted.
	var found db.App
	err = svc.DB.First(&found, child.ID).Error
	assert.Error(t, err, "expired app with balance must be deleted after cleanup")

	// Parent must have received the funds.
	parentBalance := queries.GetIsolatedBalance(svc.DB, parent.ID)
	assert.Greater(t, parentBalance, int64(0), "parent must gain balance after cleanup transfer")
}

// TestRunCashCleanup_CircleChild_PendingIncoming_DeferredNotDeleted verifies that a
// wallet with a payment still settling in is not torn down: deleting it would
// cascade-delete the pending transaction row (App.OnDelete:CASCADE), and the
// settlement would then have nowhere to credit, silently losing the funds.
func TestRunCashCleanup_CircleChild_PendingIncoming_DeferredNotDeleted(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)

	child := createSubWallet(t, svc, db.AppKindCircleWallet, parent.ID, db.ParentKindCircle, makeExpiredTime())

	pendingTx := db.Transaction{
		AppId:       &child.ID,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_PENDING,
		AmountMloki: 50_000,
		PaymentHash: "pending-incoming-hash",
	}
	require.NoError(t, svc.DB.Create(&pendingTx).Error)

	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	runCashCleanup(ctx, svc.DB, transactionsSvc, svc.LNClient, svc.EventPublisher)

	var found db.App
	require.NoError(t, svc.DB.First(&found, child.ID).Error, "wallet with a pending incoming payment must not be deleted")
	assert.False(t, found.CleanupInProgress, "cleanup_in_progress must stay false so the next tick retries")

	// The pending transaction must survive untouched (not cascade-deleted).
	var stillPending db.Transaction
	require.NoError(t, svc.DB.Where("payment_hash = ?", "pending-incoming-hash").First(&stillPending).Error)
}

// TestRunCashCleanup_ParentDeleted_BalanceWrittenOffNoFKError reproduces a
// production incident: a cash_hub was deleted while it still had an expired
// cash_wallet child with balance. On every 5-minute tick, cleanup tried to
// insert a reclaim transaction crediting the (now nonexistent) parent app,
// which violated the apps table's FOREIGN KEY constraint and retried forever,
// stranding the sub-wallet and spamming error logs. Since apps.DeleteApp now
// refuses to delete a hub with live children (see apps_service_test.go), this
// can only happen via a pre-existing orphaned row (e.g. from before that
// guard existed) — simulated here with a direct DB delete of the parent.
// Cleanup must write off the balance and delete the child instead of erroring.
func TestRunCashCleanup_ParentDeleted_BalanceWrittenOffNoFKError(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCashHub, nil, "", nil)
	require.NoError(t, err)

	child := createSubWallet(t, svc, db.AppKindCashWallet, parent.ID, db.ParentKindCash, makeExpiredTime())
	tests.FundApp(svc, child.ID, 200_000, "orphan-parent-fund-hash")
	assert.Equal(t, int64(200_000), queries.GetIsolatedBalance(svc.DB, child.ID))

	// Simulate the parent having been deleted out from under the child
	// (orphaning it), bypassing the app-level guard the way old/bad data would.
	require.NoError(t, svc.DB.Delete(&db.App{}, parent.ID).Error)

	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	runCashCleanup(ctx, svc.DB, transactionsSvc, svc.LNClient, svc.EventPublisher)

	// Child must be deleted despite the missing parent — no infinite retry, no FK error.
	var found db.App
	err = svc.DB.First(&found, child.ID).Error
	assert.Error(t, err, "orphaned expired app must still be deleted, not stuck retrying forever")
}

// A failure inside the bill's delete transaction, here the archive write,
// rolls the sweep back with it: the bill stands with its balance, the Hub got
// nothing, and the next tick retries.
func TestRunCashCleanup_AFailureInTheDeleteLeavesTheBillWhole(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCashHub, nil, "", nil)
	require.NoError(t, err)
	child := createSubWallet(t, svc, db.AppKindCashWallet, parent.ID, db.ParentKindCash, makeExpiredTime())
	tests.FundApp(svc, child.ID, 200_000, "e7-fund-hash")

	failArchive := true
	require.NoError(t, svc.DB.Callback().Create().Before("gorm:create").Register("test:fail_bill_archive", func(tx *gorm.DB) {
		if failArchive && tx.Statement.Table == "cash_bill_archives" {
			_ = tx.AddError(errors.New("archive write failed"))
		}
	}))
	t.Cleanup(func() { _ = svc.DB.Callback().Create().Remove("test:fail_bill_archive") })

	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	runCashCleanup(ctx, svc.DB, transactionsSvc, svc.LNClient, svc.EventPublisher)

	var found db.App
	require.NoError(t, svc.DB.First(&found, child.ID).Error, "the bill is not deleted")
	assert.False(t, found.CleanupInProgress, "the next tick can retry")
	assert.Equal(t, int64(200_000), queries.GetIsolatedBalance(svc.DB, child.ID), "the sweep was rolled back")
	assert.Zero(t, queries.GetIsolatedBalance(svc.DB, parent.ID))

	failArchive = false
	runCashCleanup(ctx, svc.DB, transactionsSvc, svc.LNClient, svc.EventPublisher)
	assert.Error(t, svc.DB.First(&found, child.ID).Error, "the retry deletes it")
	assert.Equal(t, int64(200_000), queries.GetIsolatedBalance(svc.DB, parent.ID))
}
