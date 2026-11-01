package swaps

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/transactions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// stubTxService satisfies transactions.TransactionsService via the embedded
// interface (nil) and overrides only LookupTransaction, which is all
// shouldPayHoldInvoice calls. Any other method would panic — intentionally,
// since the tests never reach one.
type stubTxService struct {
	transactions.TransactionsService
	lookupTx  *transactions.Transaction
	lookupErr error
}

func (s *stubTxService) LookupTransaction(_ context.Context, _ string, _ *string, _ lnclient.LNClient, _ *uint) (*transactions.Transaction, error) {
	return s.lookupTx, s.lookupErr
}

func newSwapTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&db.Swap{}))
	return gdb
}

// LOKIHUB-0025: a re-delivered swap.created (a transaction already exists for
// the payment hash) must be a no-op, so the hold-invoice payment is never
// re-attempted and the swap is never marked FAILED by a spurious re-pay error.
func TestShouldPayHoldInvoice_Idempotent(t *testing.T) {
	svc := newTestSwapsService(nil)
	svc.ctx = context.Background()

	// A transaction already exists for this hash (lookup returns no error):
	// an earlier swap.created already initiated the payment -> no-op.
	svc.transactionsService = &stubTxService{lookupTx: &transactions.Transaction{}}
	assert.False(t, svc.shouldPayHoldInvoice(&db.Swap{SwapId: "s1", PaymentHash: "h"}),
		"a re-delivered swap.created with an existing tx must not re-pay")

	// First delivery: no transaction yet (not-found) -> pay.
	svc.transactionsService = &stubTxService{lookupErr: transactions.NewNotFoundError()}
	assert.True(t, svc.shouldPayHoldInvoice(&db.Swap{SwapId: "s1", PaymentHash: "h"}),
		"the first swap.created must initiate the hold-invoice payment")

	// Unexpected lookup error -> conservative no-op (never a spurious failure).
	svc.transactionsService = &stubTxService{lookupErr: errors.New("db unavailable")}
	assert.False(t, svc.shouldPayHoldInvoice(&db.Swap{SwapId: "s1", PaymentHash: "h"}),
		"an unexpected lookup error must not trigger a re-pay")
}

// LOKIHUB-0025: markSwapState must never regress a terminal state — a duplicate
// swap.created arriving after the on-chain claim settled must not overwrite a
// recorded SUCCESS with FAILED.
func TestMarkSwapState_DoesNotRegressTerminalSuccess(t *testing.T) {
	gdb := newSwapTestDB(t)
	svc := newTestSwapsService(nil)
	svc.db = gdb

	s := &db.Swap{SwapId: "swap-success", State: constants.SWAP_STATE_SUCCESS}
	require.NoError(t, gdb.Create(s).Error)

	svc.markSwapState(s, constants.SWAP_STATE_FAILED)

	var got db.Swap
	require.NoError(t, gdb.First(&got, "swap_id = ?", "swap-success").Error)
	assert.Equal(t, constants.SWAP_STATE_SUCCESS, got.State,
		"a completed SUCCESS must not be regressed to FAILED")
}

// markSwapState still advances a non-terminal swap normally.
func TestMarkSwapState_AdvancesPendingSwap(t *testing.T) {
	gdb := newSwapTestDB(t)
	svc := newTestSwapsService(nil)
	svc.db = gdb

	p := &db.Swap{SwapId: "swap-pending", State: constants.SWAP_STATE_PENDING}
	require.NoError(t, gdb.Create(p).Error)

	svc.markSwapState(p, constants.SWAP_STATE_SUCCESS)

	var got db.Swap
	require.NoError(t, gdb.First(&got, "swap_id = ?", "swap-pending").Error)
	assert.Equal(t, constants.SWAP_STATE_SUCCESS, got.State,
		"a PENDING swap must still advance to SUCCESS")
}
