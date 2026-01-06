package transactions

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestLookupTransaction_IncomingPayment(t *testing.T) {
	ctx := context.TODO()

	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	mockPreimage := tests.MockLNClientTransaction.Preimage
	svc.DB.Create(&db.Transaction{
		State:          constants.TRANSACTION_STATE_PENDING,
		Type:           constants.TRANSACTION_TYPE_INCOMING,
		PaymentRequest: tests.MockLNClientTransaction.Invoice,
		PaymentHash:    tests.MockLNClientTransaction.PaymentHash,
		Preimage:       &mockPreimage,
		AmountMloki:    123000,
	})

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)

	incomingTransaction, err := transactionsService.LookupTransaction(ctx, tests.MockLNClientTransaction.PaymentHash, nil, svc.LNClient, nil)
	assert.NoError(t, err)
	assert.Equal(t, uint64(123000), incomingTransaction.AmountMloki)
	assert.Equal(t, constants.TRANSACTION_STATE_PENDING, incomingTransaction.State)
	assert.Equal(t, tests.MockLNClientTransaction.Preimage, *incomingTransaction.Preimage)
	assert.Zero(t, incomingTransaction.FeeReserveMsat)
}

func TestLookupTransaction_OutgoingPayment(t *testing.T) {
	ctx := context.TODO()

	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	mockPreimage := tests.MockLNClientTransaction.Preimage
	svc.DB.Create(&db.Transaction{
		State:          constants.TRANSACTION_STATE_PENDING,
		Type:           constants.TRANSACTION_TYPE_OUTGOING,
		PaymentRequest: tests.MockLNClientTransaction.Invoice,
		PaymentHash:    tests.MockLNClientTransaction.PaymentHash,
		Preimage:       &mockPreimage,
		AmountMloki:    123000,
	})

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)

	outgoingTransaction, err := transactionsService.LookupTransaction(ctx, tests.MockLNClientTransaction.PaymentHash, nil, svc.LNClient, nil)
	assert.NoError(t, err)
	assert.Equal(t, uint64(123000), outgoingTransaction.AmountMloki)
	assert.Equal(t, constants.TRANSACTION_STATE_PENDING, outgoingTransaction.State)
	assert.Equal(t, tests.MockLNClientTransaction.Preimage, *outgoingTransaction.Preimage)
	assert.Zero(t, outgoingTransaction.FeeReserveMsat)
}
