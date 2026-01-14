package queries

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestGetIsolatedBalance_PendingNoOverflow(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)
	app.Isolated = true
	svc.DB.Save(&app)

	paymentAmount := uint64(1000)

	tx := db.Transaction{
		AppId:           &app.ID,
		RequestEventId:  nil,
		Type:            constants.TRANSACTION_TYPE_OUTGOING,
		State:           constants.TRANSACTION_STATE_PENDING,
		FeeReserveMloki: uint64(10000),
		AmountMloki:     paymentAmount,
		PaymentRequest:  tests.MockInvoice,
		PaymentHash:     tests.MockPaymentHash,
		SelfPayment:     true,
	}
	svc.DB.Save(&tx)

	balance := GetIsolatedBalance(svc.DB, app.ID)
	assert.Equal(t, int64(-11000), balance)
}

func TestGetIsolatedBalance_SettledNoOverflow(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)
	app.Isolated = true
	svc.DB.Save(&app)

	paymentAmount := uint64(1000)

	tx := db.Transaction{
		AppId:           &app.ID,
		RequestEventId:  nil,
		Type:            constants.TRANSACTION_TYPE_OUTGOING,
		State:           constants.TRANSACTION_STATE_SETTLED,
		FeeReserveMloki: uint64(0),
		AmountMloki:     paymentAmount,
		PaymentRequest:  tests.MockInvoice,
		PaymentHash:     tests.MockPaymentHash,
		SelfPayment:     true,
	}
	svc.DB.Save(&tx)

	balance := GetIsolatedBalance(svc.DB, app.ID)
	assert.Equal(t, int64(-1000), balance)
}
