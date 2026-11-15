package transactions

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// A node that reports success without a preimage has still paid. SendPaymentSync
// must say so distinctly, so callers that roll back on failure don't, and must
// leave the row PENDING (not FAILED) for reconciliation.
func TestSendPaymentSync_SucceededWithoutPreimage_IsSentNotRecorded(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	mockLn := svc.LNClient.(*tests.MockLn)
	mockLn.PayInvoiceResponses = append(mockLn.PayInvoiceResponses, &lnclient.PayInvoiceResponse{Preimage: ""})
	mockLn.PayInvoiceErrors = append(mockLn.PayInvoiceErrors, nil)

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendPaymentSync(tests.MockLNClientTransaction.Invoice, nil, nil, svc.LNClient, nil, nil)
	require.Nil(t, transaction)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPaymentSentNotRecorded), "got %v", err)
	require.False(t, errors.Is(err, lnclient.ErrPaymentPending))

	var row db.Transaction
	require.NoError(t, svc.DB.Where(&db.Transaction{
		Type: constants.TRANSACTION_TYPE_OUTGOING, PaymentHash: tests.MockLNClientTransaction.PaymentHash,
	}).First(&row).Error)
	require.Equal(t, constants.TRANSACTION_STATE_PENDING, row.State)
}
