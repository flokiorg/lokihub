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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
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
