package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// failingCancelNode refuses every invoice cancel, and records each it was asked.
type failingCancelNode struct {
	*tests.MockLn
	mu    sync.Mutex
	asked []string
}

func (n *failingCancelNode) CancelHoldInvoice(_ context.Context, paymentHash string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.asked = append(n.asked, paymentHash)
	return errors.New("node refuses")
}

// A node that keeps refusing to cancel a closing wallet's open invoice does not
// keep the wallet from being deleted: the delete fails the row and asks the node
// again after commit. A wallet with no open invoice costs the node nothing.
func TestRunCircleLifecycle_NodeCancelFailureDoesNotBlockTheDelete(t *testing.T) {
	f := newLifecycleFixture(t)
	now := time.Now()
	future := now.Add(time.Hour)
	closing := f.wallet(t, circlelife.StateClosing, nil, now.Unix()-1, 0)
	quiet := f.wallet(t, circlelife.StateClosing, nil, now.Unix()+600, 0)
	hash := tests.RandomHex32()
	require.NoError(t, f.svc.DB.Create(&db.Transaction{AppId: &closing.ID, Type: constants.TRANSACTION_TYPE_INCOMING,
		State: constants.TRANSACTION_STATE_PENDING, AmountMloki: 500, ExpiresAt: &future, PaymentHash: hash, Hold: true,
		PaymentRequest: "lnfc1issued"}).Error)
	ln, ok := f.svc.LNClient.(*tests.MockLn)
	require.True(t, ok)
	node := &failingCancelNode{MockLn: ln}

	result := RunCircleLifecycle(context.TODO(), f.svc.DB, f.ts, node, f.svc.EventPublisher, now)
	assert.Equal(t, 1, result.Deleted)
	assert.Equal(t, "deleted", f.state(t, closing))
	assert.Equal(t, circlelife.StateClosing, f.state(t, quiet))
	assert.Equal(t, []string{hash, hash}, node.asked, "asked before the delete and again after its commit, never for the quiet wallet")

	node.asked = nil
	RunCircleLifecycle(context.TODO(), f.svc.DB, f.ts, node, f.svc.EventPublisher, now)
	assert.Empty(t, node.asked, "nothing open is left to cancel")
}
