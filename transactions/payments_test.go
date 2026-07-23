package transactions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

func TestSendPaymentSync_NoApp(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	metadata := map[string]interface{}{
		"a": 123,
	}

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendPaymentSync(tests.MockLNClientTransaction.Invoice, nil, metadata, svc.LNClient, nil, nil)

	assert.NoError(t, err)
	assert.Equal(t, uint64(123000), transaction.AmountMloki)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, transaction.State)
	assert.Zero(t, transaction.FeeReserveMloki)
	assert.Equal(t, "123preimage", *transaction.Preimage)

	type dummyMetadata struct {
		A int `json:"a"`
	}
	var decodedMetadata dummyMetadata
	err = json.Unmarshal(transaction.Metadata, &decodedMetadata)
	assert.NoError(t, err)
	assert.Equal(t, 123, decodedMetadata.A)
}

func TestSendPaymentSync_ZeroAmount(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	metadata := map[string]interface{}{
		"a": 123,
	}

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	amount := uint64(1234)
	transaction, err := transactionsService.SendPaymentSync(tests.MockZeroAmountInvoice, &amount, metadata, svc.LNClient, nil, nil)

	assert.NoError(t, err)
	assert.Equal(t, amount, transaction.AmountMloki)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, transaction.State)
	assert.Zero(t, transaction.FeeReserveMloki)
	assert.Equal(t, "123preimage", *transaction.Preimage)
}

func TestSendPaymentSync_AmountOnNonZeroAmountInvoice(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	metadata := map[string]interface{}{
		"a": 123,
	}

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	amount := uint64(1234)
	transaction, err := transactionsService.SendPaymentSync(tests.MockInvoice, &amount, metadata, svc.LNClient, nil, nil)

	assert.NoError(t, err)
	// amount is from the invoice, not what was specified
	assert.Equal(t, uint64(123_000), transaction.AmountMloki)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, transaction.State)
	assert.Zero(t, transaction.FeeReserveMloki)
	assert.Equal(t, "123preimage", *transaction.Preimage)
}

func TestSendPaymentSync_MetadataTooLarge(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	metadata := make(map[string]interface{})
	metadata["randomkey"] = strings.Repeat("a", constants.INVOICE_METADATA_MAX_LENGTH-15) // json encoding adds 16 characters

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendPaymentSync(tests.MockLNClientTransaction.Invoice, nil, metadata, svc.LNClient, nil, nil)

	assert.Error(t, err)
	assert.Equal(t, fmt.Sprintf("encoded payment metadata provided is too large. Limit: %d Received: %d", constants.INVOICE_METADATA_MAX_LENGTH, constants.INVOICE_METADATA_MAX_LENGTH+1), err.Error())
	assert.Nil(t, transaction)
}

func TestSendPaymentSync_Duplicate_AlreadyPaid(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.DB.Create(&db.Transaction{
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
		AmountMloki: 123000,
	})

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendPaymentSync(tests.MockLNClientTransaction.Invoice, nil, nil, svc.LNClient, nil, nil)

	assert.Error(t, err)
	assert.Equal(t, "this invoice has already been paid", err.Error())
	assert.Nil(t, transaction)
}

func TestSendPaymentSync_Duplicate_Pending(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.DB.Create(&db.Transaction{
		State:       constants.TRANSACTION_STATE_PENDING,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
		AmountMloki: 123000,
	})

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendPaymentSync(tests.MockLNClientTransaction.Invoice, nil, nil, svc.LNClient, nil, nil)

	assert.Error(t, err)
	assert.Equal(t, "there is already a payment pending for this invoice", err.Error())
	assert.Nil(t, transaction)
}

func TestSendPaymentSync_Duplicate_Failed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.DB.Create(&db.Transaction{
		State:       constants.TRANSACTION_STATE_FAILED,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
		AmountMloki: 123000,
	})

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	_, err = transactionsService.SendPaymentSync(tests.MockLNClientTransaction.Invoice, nil, nil, svc.LNClient, nil, nil)

	assert.NoError(t, err)
}

func TestMarkSettled_Sent(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	dbTransaction := db.Transaction{
		State:       constants.TRANSACTION_STATE_PENDING,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
		AmountMloki: 123000,
	}
	svc.DB.Create(&dbTransaction)

	mockEventConsumer := tests.NewMockEventConsumer()
	svc.EventPublisher.RegisterSubscriber(mockEventConsumer)
	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	var settled *db.Transaction
	err = svc.DB.Transaction(func(tx *gorm.DB) error {
		settled, err = transactionsService.markTransactionSettled(tx, &dbTransaction, "test", 0, false)
		return err
	})
	if settled != nil {
		transactionsService.publishSettleEvent(settled)
	}

	assert.NoError(t, err)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, dbTransaction.State)
	assert.Equal(t, 1, len(mockEventConsumer.GetConsumedEvents()))
	assert.Equal(t, "nwc_payment_sent", mockEventConsumer.GetConsumedEvents()[0].Event)
	settledTransaction := mockEventConsumer.GetConsumedEvents()[0].Properties.(*db.Transaction)
	assert.Equal(t, &dbTransaction, settledTransaction)
}

func TestMarkSettled_Twice(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	dbTransaction := db.Transaction{
		State:       constants.TRANSACTION_STATE_PENDING,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
		AmountMloki: 123000,
	}
	svc.DB.Create(&dbTransaction)

	mockEventConsumer := tests.NewMockEventConsumer()
	svc.EventPublisher.RegisterSubscriber(mockEventConsumer)
	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	var wg sync.WaitGroup
	n := 10
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			var settled *db.Transaction
			// txErr is goroutine-local - the outer err (from CreateTestService,
			// already asserted above) must not be written from multiple
			// goroutines at once.
			txErr := svc.DB.Transaction(func(tx *gorm.DB) error {
				time.Sleep(time.Duration(n) * 10 * time.Millisecond)
				var markErr error
				settled, markErr = transactionsService.markTransactionSettled(tx, &dbTransaction, "test", 0, false)
				time.Sleep(time.Duration(n) * 10 * time.Millisecond)
				return markErr
			})
			if settled != nil {
				transactionsService.publishSettleEvent(settled)
			}
			require.NoError(t, txErr)
		}()
	}
	wg.Wait()

	// ensure we only mark transaction settled once and only fire
	// settled notifications once
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, dbTransaction.State)
	assert.Equal(t, 1, len(mockEventConsumer.GetConsumedEvents()))
	assert.Equal(t, "nwc_payment_sent", mockEventConsumer.GetConsumedEvents()[0].Event)
	settledTransaction := mockEventConsumer.GetConsumedEvents()[0].Properties.(*db.Transaction)
	assert.Equal(t, &dbTransaction, settledTransaction)
}

func TestMarkSettled_Received(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	dbTransaction := db.Transaction{
		State:       constants.TRANSACTION_STATE_PENDING,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
		AmountMloki: 123000,
	}
	svc.DB.Create(&dbTransaction)

	mockEventConsumer := tests.NewMockEventConsumer()
	svc.EventPublisher.RegisterSubscriber(mockEventConsumer)
	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	var settled *db.Transaction
	err = svc.DB.Transaction(func(tx *gorm.DB) error {
		settled, err = transactionsService.markTransactionSettled(tx, &dbTransaction, "test", 0, false)
		return err
	})
	if settled != nil {
		transactionsService.publishSettleEvent(settled)
	}

	assert.NoError(t, err)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, dbTransaction.State)
	assert.Equal(t, 1, len(mockEventConsumer.GetConsumedEvents()))
	assert.Equal(t, "nwc_payment_received", mockEventConsumer.GetConsumedEvents()[0].Event)
	settledTransaction := mockEventConsumer.GetConsumedEvents()[0].Properties.(*db.Transaction)
	assert.Equal(t, &dbTransaction, settledTransaction)
}

func TestDoNotMarkSettledTwice(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	settledAt := time.Now().Add(time.Duration(-1) * time.Minute)
	dbTransaction := db.Transaction{
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
		AmountMloki: 123000,
		SettledAt:   &settledAt,
	}
	svc.DB.Create(&dbTransaction)

	mockEventConsumer := tests.NewMockEventConsumer()
	svc.EventPublisher.RegisterSubscriber(mockEventConsumer)
	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	err = svc.DB.Transaction(func(tx *gorm.DB) error {
		_, err = transactionsService.markTransactionSettled(tx, &dbTransaction, "test", 0, false)
		return err
	})

	assert.NoError(t, err)
	assert.Zero(t, len(mockEventConsumer.GetConsumedEvents()))
}

func TestMarkFailed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	dbTransaction := db.Transaction{
		State:       constants.TRANSACTION_STATE_PENDING,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
		AmountMloki: 123000,
	}
	svc.DB.Create(&dbTransaction)

	mockEventConsumer := tests.NewMockEventConsumer()
	svc.EventPublisher.RegisterSubscriber(mockEventConsumer)
	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	err = svc.DB.Transaction(func(tx *gorm.DB) error {
		_, markErr := transactionsService.markPaymentFailed(tx, &dbTransaction, "some routing error")
		return markErr
	})

	assert.NoError(t, err)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, dbTransaction.State)
	assert.Equal(t, 1, len(mockEventConsumer.GetConsumedEvents()))
	assert.Equal(t, "nwc_payment_failed", mockEventConsumer.GetConsumedEvents()[0].Event)
	settledTransaction := mockEventConsumer.GetConsumedEvents()[0].Properties.(*db.Transaction)
	assert.Equal(t, &dbTransaction, settledTransaction)
	assert.Equal(t, "some routing error", settledTransaction.FailureReason)
}

func TestDoNotMarkFailedTwice(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	updatedAt := time.Now().Add(time.Duration(-1) * time.Minute)
	dbTransaction := db.Transaction{
		State:       constants.TRANSACTION_STATE_FAILED,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
		AmountMloki: 123000,
		UpdatedAt:   updatedAt,
	}
	svc.DB.Create(&dbTransaction)

	mockEventConsumer := tests.NewMockEventConsumer()
	svc.EventPublisher.RegisterSubscriber(mockEventConsumer)
	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	err = svc.DB.Transaction(func(tx *gorm.DB) error {
		_, markErr := transactionsService.markPaymentFailed(tx, &dbTransaction, "some routing error")
		return markErr
	})

	assert.NoError(t, err)
	assert.Equal(t, updatedAt, dbTransaction.UpdatedAt)
	assert.Zero(t, len(mockEventConsumer.GetConsumedEvents()))
}

// TestMarkPaymentFailed_DoesNotDowngradeSettled is the direct regression test
// for the settle-vs-fail race: if the async payment-sent-event subscription
// settles a transaction before the synchronous SendPaymentSync error branch
// gets a chance to run, markPaymentFailed must never flip that row back to
// FAILED - the funds already left the node, and get_isolated_balance only
// sums SETTLED rows, so downgrading it would silently drop the paid-out
// amount from balance accounting and (for a JIT claim) reopen the slice.
func TestMarkPaymentFailed_DoesNotDowngradeSettled(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	settledAt := time.Now().Add(time.Duration(-1) * time.Minute)
	preimage := "123preimage"
	dbTransaction := db.Transaction{
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
		AmountMloki: 123000,
		Preimage:    &preimage,
		SettledAt:   &settledAt,
	}
	svc.DB.Create(&dbTransaction)

	mockEventConsumer := tests.NewMockEventConsumer()
	svc.EventPublisher.RegisterSubscriber(mockEventConsumer)
	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)

	var failedTransaction *db.Transaction
	err = svc.DB.Transaction(func(tx *gorm.DB) error {
		var markErr error
		failedTransaction, markErr = transactionsService.markPaymentFailed(tx, &dbTransaction, "late RPC timeout error")
		return markErr
	})

	assert.NoError(t, err)
	assert.Nil(t, failedTransaction)
	assert.Zero(t, len(mockEventConsumer.GetConsumedEvents()))

	var reloaded db.Transaction
	require.NoError(t, svc.DB.First(&reloaded, dbTransaction.ID).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, reloaded.State)
	assert.Equal(t, preimage, *reloaded.Preimage)
	assert.NotNil(t, reloaded.SettledAt)
	assert.Empty(t, reloaded.FailureReason)
}

// TestMarkPaymentFailed_DoesNotDowngradeSettled_IncomingTransaction covers the
// same guard for incoming (hold-invoice) transactions, since markPaymentFailed
// is also reached from the hold-invoice-cancellation path (CancelHoldInvoice).
func TestMarkPaymentFailed_DoesNotDowngradeSettled_IncomingTransaction(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	settledAt := time.Now().Add(time.Duration(-1) * time.Minute)
	preimage := "123preimage"
	dbTransaction := db.Transaction{
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
		AmountMloki: 123000,
		Preimage:    &preimage,
		SettledAt:   &settledAt,
	}
	svc.DB.Create(&dbTransaction)

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)

	var failedTransaction *db.Transaction
	err = svc.DB.Transaction(func(tx *gorm.DB) error {
		var markErr error
		failedTransaction, markErr = transactionsService.markPaymentFailed(tx, &dbTransaction, "hold invoice cancellation raced settlement")
		return markErr
	})

	assert.NoError(t, err)
	assert.Nil(t, failedTransaction)

	var reloaded db.Transaction
	require.NoError(t, svc.DB.First(&reloaded, dbTransaction.ID).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, reloaded.State)
}

// TestMarkTransactionSettled_UpgradesPreviouslyFailed documents and locks in
// the intentionally *asymmetric* counterpart to the two tests above: unlike
// markPaymentFailed (never downgrades SETTLED -> FAILED), markTransactionSettled
// deliberately has no guard against settling an already-FAILED row - this is
// desired self-healing behavior (see TestConsumeEvent_FailedMarkedAsSuccessful),
// not a second instance of the same bug class, because upgrading a stale FAILED
// marking to SETTLED once real settlement is observed can only ever correct the
// ledger in the safe direction (crediting funds that did leave the node), while
// the reverse (downgrading a real SETTLED to FAILED) would hide funds that did.
func TestMarkTransactionSettled_UpgradesPreviouslyFailed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	dbTransaction := db.Transaction{
		State:       constants.TRANSACTION_STATE_FAILED,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
		AmountMloki: 123000,
	}
	svc.DB.Create(&dbTransaction)

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)

	var settled *db.Transaction
	err = svc.DB.Transaction(func(tx *gorm.DB) error {
		var settleErr error
		settled, settleErr = transactionsService.markTransactionSettled(tx, &dbTransaction, "123preimage", 0, false)
		return settleErr
	})

	assert.NoError(t, err)
	require.NotNil(t, settled)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, settled.State)
}

func TestSendPaymentSync_FailedRemovesFeeReserve(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).PayInvoiceErrors = append(svc.LNClient.(*tests.MockLn).PayInvoiceErrors, errors.New("Some error"))
	svc.LNClient.(*tests.MockLn).PayInvoiceResponses = append(svc.LNClient.(*tests.MockLn).PayInvoiceResponses, nil)

	mockEventConsumer := tests.NewMockEventConsumer()
	svc.EventPublisher.RegisterSubscriber(mockEventConsumer)

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendPaymentSync(tests.MockLNClientTransaction.Invoice, nil, nil, svc.LNClient, nil, nil)

	assert.Error(t, err)
	assert.Nil(t, transaction)

	transactionType := constants.TRANSACTION_TYPE_OUTGOING
	transaction, err = transactionsService.LookupTransaction(context.TODO(), tests.MockLNClientTransaction.PaymentHash, &transactionType, svc.LNClient, nil)
	assert.NoError(t, err)

	assert.Equal(t, uint64(123000), transaction.AmountMloki)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, transaction.State)
	assert.Zero(t, transaction.FeeReserveMloki)
	assert.Nil(t, transaction.Preimage)

	assert.Equal(t, 1, len(mockEventConsumer.GetConsumedEvents()))
	assert.Equal(t, "nwc_payment_failed", mockEventConsumer.GetConsumedEvents()[0].Event)
}

func TestSendPaymentSync_PendingHasFeeReserve(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// fake a delay to ensure the payment is still pending
	delay := 10 * time.Second
	svc.LNClient.(*tests.MockLn).PaymentDelay = &delay

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	go func() {
		_, _ = transactionsService.SendPaymentSync(tests.MockLNClientTransaction.Invoice, nil, nil, svc.LNClient, nil, nil)
	}()
	// ensure the goroutine above runs first
	time.Sleep(10 * time.Millisecond)

	transactionType := constants.TRANSACTION_TYPE_OUTGOING
	transaction, err := transactionsService.LookupTransaction(context.TODO(), tests.MockLNClientTransaction.PaymentHash, &transactionType, svc.LNClient, nil)
	assert.NoError(t, err)

	assert.Equal(t, uint64(123000), transaction.AmountMloki)
	assert.Equal(t, constants.TRANSACTION_STATE_PENDING, transaction.State)
	assert.Equal(t, uint64(10000), transaction.FeeReserveMloki)
	assert.Nil(t, transaction.Preimage)
}

// TestSendPaymentSync_SettleRacesFailure_ReturnsSettled is the end-to-end
// regression test for the settle-vs-fail race, exercised through the real
// SendPaymentSync call (not just the markPaymentFailed unit tests above).
// The synchronous RPC call is delayed and configured to ultimately error; while
// it's in flight, an async "nwc_lnclient_payment_sent" event (the same kind a
// real subscribePayments-style goroutine would emit) settles the same payment
// hash first. SendPaymentSync must not surface the stale RPC error for a
// payment that actually went out - it must return the settled transaction
// with no error, and the isolated balance must reflect the amount as spent,
// not silently reappear as available for a second payment/claim.
func TestSendPaymentSync_SettleRacesFailure_ReturnsSettled(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	delay := 100 * time.Millisecond
	mockLn := svc.LNClient.(*tests.MockLn)
	mockLn.PaymentDelay = &delay
	mockLn.PayInvoiceErrors = append(mockLn.PayInvoiceErrors, errors.New("timeout talking to node"))
	mockLn.PayInvoiceResponses = append(mockLn.PayInvoiceResponses, nil)

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)

	var wg sync.WaitGroup
	var result *Transaction
	var sendErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, sendErr = transactionsService.SendPaymentSync(tests.MockLNClientTransaction.Invoice, nil, nil, svc.LNClient, nil, nil)
	}()

	// Wait for the goroutine to actually create the PENDING row before firing
	// the settle event - a fixed sleep here is flaky under load (e.g. running
	// the full test suite in parallel), since it races the same 100ms mock
	// delay this test depends on to open the window in the first place.
	require.Eventually(t, func() bool {
		var pending db.Transaction
		return svc.DB.Where(&db.Transaction{
			Type:        constants.TRANSACTION_TYPE_OUTGOING,
			State:       constants.TRANSACTION_STATE_PENDING,
			PaymentHash: tests.MockLNClientTransaction.PaymentHash,
		}).First(&pending).Error == nil
	}, 2*time.Second, 2*time.Millisecond, "PENDING transaction row was never created")

	transactionsService.ConsumeEvent(context.TODO(), &events.Event{
		Event: "nwc_lnclient_payment_sent",
		Properties: &lnclient.Transaction{
			Type:            tests.MockLNClientTransaction.Type,
			Invoice:         tests.MockLNClientTransaction.Invoice,
			Description:     tests.MockLNClientTransaction.Description,
			DescriptionHash: tests.MockLNClientTransaction.DescriptionHash,
			Preimage:        tests.MockLNClientTransaction.Preimage,
			PaymentHash:     tests.MockLNClientTransaction.PaymentHash,
			Amount:          tests.MockLNClientTransaction.Amount,
			FeesPaid:        tests.MockLNClientTransaction.FeesPaid,
		},
	}, nil)

	wg.Wait()

	assert.NoError(t, sendErr)
	require.NotNil(t, result)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, result.State)

	var reloaded db.Transaction
	require.NoError(t, svc.DB.First(&reloaded, result.ID).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, reloaded.State)
}

func TestConsumeEvent_FailedMarkedAsSuccessful(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)

	svc.LNClient.(*tests.MockLn).PayInvoiceErrors = append(svc.LNClient.(*tests.MockLn).PayInvoiceErrors, errors.New("some error"))
	svc.LNClient.(*tests.MockLn).PayInvoiceResponses = append(svc.LNClient.(*tests.MockLn).PayInvoiceResponses, nil)

	transaction, err := transactionsService.SendPaymentSync(tests.MockLNClientTransaction.Invoice, nil, nil, svc.LNClient, nil, nil)

	assert.Error(t, err)
	assert.Nil(t, transaction)

	var transactions []db.Transaction
	result := svc.DB.Find(&transactions, &db.Transaction{
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
	})
	assert.NoError(t, result.Error)
	assert.Equal(t, 1, len(transactions))

	transaction = &transactions[0]
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, transaction.State)

	// Now that we have a failed transaction, we submit a "nwc_lnclient_payment_sent" event.
	// This should be marked as successful as long as there are no pending payments for the
	// same payment hash

	transactionsService.ConsumeEvent(context.TODO(), &events.Event{
		Event: "nwc_lnclient_payment_sent",
		Properties: &lnclient.Transaction{
			Type:            tests.MockLNClientTransaction.Type,
			Invoice:         tests.MockLNClientTransaction.Invoice,
			Description:     tests.MockLNClientTransaction.Description,
			DescriptionHash: tests.MockLNClientTransaction.DescriptionHash,
			Preimage:        tests.MockLNClientTransaction.Preimage,
			PaymentHash:     tests.MockLNClientTransaction.PaymentHash,
			Amount:          tests.MockLNClientTransaction.Amount,
			FeesPaid:        tests.MockLNClientTransaction.FeesPaid,
		},
	}, nil)

	// Re-read transactions and ensure that the single returned transaction
	// is now settled.
	result = svc.DB.Find(&transactions, &db.Transaction{
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: tests.MockLNClientTransaction.PaymentHash,
	})
	assert.NoError(t, result.Error)
	assert.Equal(t, 1, len(transactions))

	transaction = &transactions[0]
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, transaction.State)
}
