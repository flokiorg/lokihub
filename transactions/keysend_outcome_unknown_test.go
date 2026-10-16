package transactions

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// A keysend whose outcome the node could not report may still settle. Marking
// it failed would credit the payer back with money that may have left; it stays
// PENDING for the payment subscription or the stale sweep's lookup to resolve.
func TestSendKeysend_OutcomeUnknown_StaysPending(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).SendKeysendError =
		fmt.Errorf("%w: rpc error: code = Canceled desc = context canceled", lnclient.ErrPaymentPending)

	transaction, err := NewTransactionsService(svc.DB, svc.EventPublisher).
		SendKeysend(uint64(1000), "fake destination", nil, "", svc.LNClient, nil, nil)
	require.Nil(t, transaction)
	require.True(t, errors.Is(err, lnclient.ErrPaymentPending), "got %v", err)

	var rows []db.Transaction
	require.NoError(t, svc.DB.Where("type = ?", constants.TRANSACTION_TYPE_OUTGOING).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, constants.TRANSACTION_STATE_PENDING, rows[0].State)
}

// A definite keysend failure is still recorded as failed.
func TestSendKeysend_DefiniteFailure_MarkedFailed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).SendKeysendError = errors.New("FAILURE_REASON_NO_ROUTE")

	_, err = NewTransactionsService(svc.DB, svc.EventPublisher).
		SendKeysend(uint64(1000), "fake destination", nil, "", svc.LNClient, nil, nil)
	require.Error(t, err)

	var rows []db.Transaction
	require.NoError(t, svc.DB.Where("type = ?", constants.TRANSACTION_TYPE_OUTGOING).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, constants.TRANSACTION_STATE_FAILED, rows[0].State)
}
