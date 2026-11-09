package transactions

// F1 — SweepStalePendingOutgoing silently force-marks FAILED even when
// LookupInvoice fails (node unreachable).  Correct behaviour: if the LN node
// is offline and the payment state is unknown, the transaction must NOT be
// force-moved to FAILED.
//
// This test asserts the correct behaviour.  It FAILS today because the sweep
// force-marks the transaction FAILED regardless of the LN error; it will PASS
// once the fix is applied.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	testdb "github.com/flokiorg/lokihub/tests/db"
)

func TestSweepStalePendingOutgoing_LNOffline_DoesNotMarkFailed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Create a stale outgoing PENDING transaction (created 72 h ago, well past stalePendingTTL=48h).
	staleTime := time.Now().Add(-72 * time.Hour)
	staleTx := db.Transaction{
		State:       constants.TRANSACTION_STATE_PENDING,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: "aaaa" + tests.MockPaymentHash[4:], // distinct hash to avoid duplicate detection
		AmountMloki: 123000,
		CreatedAt:   staleTime,
	}
	require.NoError(t, svc.DB.Create(&staleTx).Error)
	// Back-date the record so the DB timestamp also reflects the stale window.
	require.NoError(t, svc.DB.Model(&staleTx).Update("created_at", staleTime).Error)

	mockLN := svc.LNClient.(*tests.MockLn)
	// Disable notification-based settlement so checkUnsettledTransaction runs the LookupInvoice path.
	mockLN.SupportedNotificationTypes = &[]string{}
	// Simulate the LN node being unreachable.
	mockLN.MockLookupInvoiceError = errors.New("connection refused: node offline")

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transactionsService.SweepStalePendingOutgoing(context.Background(), svc.LNClient)

	// Correct behaviour: payment state is unknown — must not be force-cancelled.
	var refreshed db.Transaction
	require.NoError(t, svc.DB.First(&refreshed, staleTx.ID).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_PENDING, refreshed.State,
		"stale tx must stay PENDING when LN node is unreachable; force-FAILED is data loss")
}

// TestSweepStalePendingOutgoing_StatementsDoNotGrowWithBacklog pins the keyset
// batching: the sweep does a bounded amount of work per tick (sweepBatchSize ×
// sweepMaxBatchesPerTick rows), so its DB cost does NOT grow with the size of the
// stale backlog. A regression that dropped the batch cap — an unbounded loop, or a
// full re-scan every tick — would make a larger backlog cost more, which this
// catches by running two over-cap backlogs and requiring the same statement count.
func TestSweepStalePendingOutgoing_StatementsDoNotGrowWithBacklog(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds thousands of stale transactions")
	}

	// The sweep processes at most this many rows per tick; both backlogs below
	// exceed it, so both do identical work regardless of how much more is waiting.
	perTickCap := sweepBatchSize * sweepMaxBatchesPerTick

	count := func(backlog int) int64 {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)
		defer svc.Remove()
		seedStalePendingOutgoing(t, svc, backlog)

		// Every row takes the same path: the node is offline, so checkUnsettled
		// leaves it PENDING and the sweep moves on. That keeps per-row work constant.
		mockLN := svc.LNClient.(*tests.MockLn)
		mockLN.SupportedNotificationTypes = &[]string{}
		mockLN.MockLookupInvoiceError = errors.New("connection refused: node offline")

		counter := testdb.CountStatements(svc.DB)
		NewTransactionsService(svc.DB, svc.EventPublisher).
			SweepStalePendingOutgoing(context.Background(), svc.LNClient)
		return counter.N()
	}

	small := count(perTickCap + 50)
	large := count(perTickCap + 450)
	require.Equal(t, small, large,
		"the sweep must do the same work for any backlog past its per-tick cap; it grew from %d to %d statements for 400 more stale rows",
		small, large)
}

// seedStalePendingOutgoing writes n outgoing PENDING transactions old enough to be
// swept (created well past stalePendingTTL), each with a distinct payment hash.
func seedStalePendingOutgoing(t *testing.T, svc *tests.TestService, n int) {
	t.Helper()
	staleTime := time.Now().Add(-72 * time.Hour)
	txns := make([]db.Transaction, n)
	for i := range txns {
		txns[i] = db.Transaction{
			State:       constants.TRANSACTION_STATE_PENDING,
			Type:        constants.TRANSACTION_TYPE_OUTGOING,
			PaymentHash: fmt.Sprintf("%064x", i),
			AmountMloki: 1000,
			CreatedAt:   staleTime,
		}
	}
	require.NoError(t, svc.DB.CreateInBatches(txns, 500).Error)
	// gorm stamps created_at to now on create, so back-date the whole batch into the
	// stale window the sweep's cutoff selects.
	require.NoError(t, svc.DB.Model(&db.Transaction{}).
		Where("type = ?", constants.TRANSACTION_TYPE_OUTGOING).
		Update("created_at", staleTime).Error)
}
