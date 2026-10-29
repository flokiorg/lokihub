package transactions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// gatedNode is a node whose MakeHoldInvoice waits for release, or fails with
// makeErr, and whose CancelHoldInvoice answers cancelErr.
type gatedNode struct {
	*tests.MockLn
	entered   chan struct{}
	release   chan struct{}
	makeErr   error
	cancelErr error
	mu        sync.Mutex
	cancelled []string
}

func newGatedNode(t *testing.T, svc *tests.TestService) *gatedNode {
	t.Helper()
	ln, ok := svc.LNClient.(*tests.MockLn)
	require.True(t, ok)
	return &gatedNode{MockLn: ln, entered: make(chan struct{}, 8), release: make(chan struct{})}
}

func (g *gatedNode) MakeHoldInvoice(ctx context.Context, amount int64, description, descriptionHash string, expiry int64, paymentHash string) (*lnclient.Transaction, error) {
	g.entered <- struct{}{}
	<-g.release
	if g.makeErr != nil {
		return nil, g.makeErr
	}
	return g.MockLn.MakeHoldInvoice(ctx, amount, description, descriptionHash, expiry, paymentHash)
}

func (g *gatedNode) CancelHoldInvoice(ctx context.Context, paymentHash string) error {
	g.mu.Lock()
	g.cancelled = append(g.cancelled, paymentHash)
	g.mu.Unlock()
	return g.cancelErr
}

func (g *gatedNode) cancels() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.cancelled...)
}

type invoiceResult struct {
	tx  *Transaction
	err error
}

func makeGated(ts *transactionsService, wallet *db.App, node lnclient.LNClient, amount uint64) chan invoiceResult {
	out := make(chan invoiceResult, 1)
	go func() {
		tx, err := ts.MakeCircleInvoice(context.TODO(), wallet, amount, "", "", 3600, nil, node, nil)
		out <- invoiceResult{tx, err}
	}()
	return out
}

func reservation(t *testing.T, svc *tests.TestService, walletID uint) db.Transaction {
	t.Helper()
	var row db.Transaction
	require.NoError(t, svc.DB.Where("app_id = ? AND type = ?", walletID, constants.TRANSACTION_TYPE_INCOMING).First(&row).Error)
	return row
}

// The node is asked for the hold invoice outside any transaction: while it is
// slow, other writes go through, and the reserved row already counts against
// the wallet's receive room. Once it answers, the row carries the invoice.
func TestMakeCircleInvoice_NodeIsAskedOutsideTheTransaction(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	node := newGatedNode(t, svc)
	done := makeGated(ts, wallet, node, 200_000)
	<-node.entered

	writeDone := make(chan error, 1)
	go func() { writeDone <- svc.DB.Create(&db.UserConfig{Key: "probe", Value: "1"}).Error }()
	select {
	case err := <-writeDone:
		require.NoError(t, err, "another write is not held by the node call")
	case <-time.After(5 * time.Second):
		close(node.release)
		t.Fatal("a write waited on the node call")
	}
	reserved := reservation(t, svc, wallet.ID)
	assert.Equal(t, constants.TRANSACTION_STATE_PENDING, reserved.State)
	assert.Empty(t, reserved.PaymentRequest, "nobody can pay a reservation")
	room, err := circlemoney.ReceiveRoom(svc.DB, wallet, time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(300_000), room, "the reservation counts on the receive cap")

	close(node.release)
	res := <-done
	require.NoError(t, res.err)
	stored := reservation(t, svc, wallet.ID)
	assert.Equal(t, res.tx.PaymentRequest, stored.PaymentRequest)
	assert.NotEmpty(t, stored.PaymentRequest)
	assert.Empty(t, node.cancels())
}

// A node that refuses the invoice leaves nothing behind.
func TestMakeCircleInvoice_NodeRefusalDropsTheReservation(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	node := newGatedNode(t, svc)
	node.makeErr = errors.New("node down")
	close(node.release)
	_, err := ts.MakeCircleInvoice(context.TODO(), wallet, 10_000, "", "", 3600, nil, node, nil)
	require.Error(t, err)
	var n int64
	require.NoError(t, svc.DB.Model(&db.Transaction{}).Where("app_id = ?", wallet.ID).Count(&n).Error)
	assert.Zero(t, n)
}

// A wallet deleted, or stopped receiving, while the node issued the invoice:
// its reservation was failed meanwhile, so the invoice is cancelled at the node
// and never handed out.
func TestMakeCircleInvoice_WalletClosedMeanwhileCancelsTheInvoiceAtTheNode(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	node := newGatedNode(t, svc)
	done := makeGated(ts, wallet, node, 10_000)
	<-node.entered
	reserved := reservation(t, svc, wallet.ID)
	require.NoError(t, svc.DB.Model(&db.Transaction{}).Where("id = ?", reserved.ID).
		Updates(map[string]interface{}{"state": constants.TRANSACTION_STATE_FAILED, "failure_reason": "circle wallet deleted"}).Error)
	close(node.release)

	res := <-done
	var refusal *circlemoney.Refusal
	require.ErrorAs(t, res.err, &refusal)
	assert.Equal(t, constants.ERROR_RESTRICTED, refusal.Code)
	assert.Equal(t, []string{reserved.PaymentHash}, node.cancels())
	assert.Empty(t, reservation(t, svc, wallet.ID).PaymentRequest)
}

// A payment the node holds for an invoice whose row is already failed is
// cancelled at once; one for an open invoice is not.
func TestHoldAccepted_ForAFailedRowIsCancelledAtTheNode(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	node := newGatedNode(t, svc)
	close(node.release)
	ts.SetLNClientProvider(func() lnclient.LNClient { return node })

	closed, err := ts.MakeCircleInvoice(context.TODO(), wallet, 10_000, "", "", 3600, nil, node, nil)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(&db.Transaction{}).Where("id = ?", closed.ID).Update("state", constants.TRANSACTION_STATE_FAILED).Error)
	acceptExternally(t, ts, closed.PaymentHash, 10_000)
	assert.Contains(t, node.cancels(), closed.PaymentHash)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, rowState(t, svc, closed.ID).State)

	open, err := ts.MakeCircleInvoice(context.TODO(), wallet, 10_000, "", "", 3600, nil, node, nil)
	require.NoError(t, err)
	acceptExternally(t, ts, open.PaymentHash, 10_000)
	assert.NotContains(t, node.cancels(), open.PaymentHash)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, rowState(t, svc, open.ID).State)
}

// An invoice the node does not know is failed as cancelled; any other node error
// stops the cancel and keeps the row open for the next try.
func TestCancelOpenCircleInvoices_UnknownInvoiceIsDone(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	node := newGatedNode(t, svc)
	close(node.release)
	inv, err := ts.MakeCircleInvoice(context.TODO(), wallet, 10_000, "", "", 3600, nil, node, nil)
	require.NoError(t, err)

	node.cancelErr = errors.New("node unreachable")
	require.Error(t, ts.CancelOpenCircleInvoices(context.TODO(), wallet.ID, node))
	assert.Equal(t, constants.TRANSACTION_STATE_PENDING, rowState(t, svc, inv.ID).State)

	node.cancelErr = lnclient.ErrInvoiceNotFound
	require.NoError(t, ts.CancelOpenCircleInvoices(context.TODO(), wallet.ID, node))
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, rowState(t, svc, inv.ID).State)

	// The post-delete cancel is best effort and takes an unknown invoice as done.
	ts.CancelHoldInvoicesAtNode(context.TODO(), []string{inv.PaymentHash, "ab"}, node)
	assert.Equal(t, 4, len(node.cancels()))
	ts.CancelHoldInvoicesAtNode(context.TODO(), []string{inv.PaymentHash}, nil)
}
