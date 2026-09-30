package transactions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	decodepay "github.com/flokiorg/lokihub/decodepay"
	"github.com/flokiorg/lokihub/tests"
)

// TestAuditAFin_AmountlessSelfPayment_DestroysTheWholeAmount
//
// SendPaymentSync takes the DEBIT amount from the caller's amountMloki override
// whenever the bolt11 carries no amount (transactions_service.go:412-415):
//
//	paymentAmount := uint64(paymentRequest.AmountMloki)
//	if amountMloki != nil && paymentRequest.AmountMloki == 0 {
//	    paymentAmount = *amountMloki
//	}
//
// but interceptSelfPayment (transactions_service.go:1262-1305) settles the
// pre-existing INCOMING row exactly as it stands, with its own recorded
// AmountMloki, and never learns the payer's amount:
//
//	settledSelfTx, txErr = svc.markTransactionSettled(tx, &incomingTransaction, ...)
//
// For an AMOUNTLESS invoice that row was created at 0. So the outgoing leg is
// debited *amountMloki and the incoming leg is credited 0: the whole payment
// amount leaves the ledger and lands nowhere.
//
// bug-present:  incoming AmountMloki == 0 while outgoing AmountMloki == 7000
// bug-absent:   both legs == 7000
func TestAuditAFin_AmountlessSelfPayment_DestroysTheWholeAmount(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	pr, err := decodepay.Decode(tests.MockZeroAmountInvoice)
	require.NoError(t, err)
	require.EqualValues(t, 0, pr.AmountMloki, "fixture must be an amountless invoice")
	// Make the invoice's payee this node: IsSelfPayment's first condition.
	svc.LNClient.(*tests.MockLn).Pubkey = pr.Payee

	payee, _, err := tests.CreateApp(svc)
	require.NoError(t, err)

	preimage := "aa11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900"
	require.NoError(t, svc.DB.Create(&db.Transaction{
		State:          constants.TRANSACTION_STATE_PENDING,
		Type:           constants.TRANSACTION_TYPE_INCOMING,
		PaymentRequest: tests.MockZeroAmountInvoice,
		PaymentHash:    tests.MockZeroAmountPaymentHash,
		Preimage:       &preimage,
		AmountMloki:    0, // what MakeInvoice records for an amountless invoice
		AppId:          &payee.ID,
	}).Error)

	const amount = uint64(7000)
	requested := amount
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	out, err := ts.SendPaymentSync(tests.MockZeroAmountInvoice, &requested, nil, svc.LNClient, nil, nil)
	require.NoError(t, err)

	assert.True(t, out.SelfPayment, "must have been intercepted as a self payment")
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, out.State)
	assert.EqualValues(t, amount, out.AmountMloki, "the DEBIT leg is the caller's amount")

	var in db.Transaction
	require.NoError(t, svc.DB.
		Where("type = ? AND payment_hash = ?", constants.TRANSACTION_TYPE_INCOMING, tests.MockZeroAmountPaymentHash).
		First(&in).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, in.State, "the incoming leg was settled")

	t.Logf("debited %d mloki, credited %d mloki, destroyed %d mloki",
		out.AmountMloki, in.AmountMloki, out.AmountMloki-in.AmountMloki)
	assert.EqualValues(t, amount, in.AmountMloki,
		"CONSERVATION: the credit leg must equal the debit leg; it is %d, so %d mloki were destroyed",
		in.AmountMloki, out.AmountMloki-in.AmountMloki)
}
