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
	app.Kind = db.AppKindIsolated
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
	app.Kind = db.AppKindIsolated
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

func TestGetIsolatedBalance_FeeSkimIncluded(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)
	app.Kind = db.AppKindCircleWallet
	svc.DB.Save(&app)

	tests.FundApp(svc, app.ID, 200_000, tests.RandomHex32())

	// A settled outgoing payment with a nonzero FeeSkimMloki (the circle_hub's
	// forwarding-fee cut) must be subtracted from the child's balance exactly
	// like FeeMloki/FeeReserveMloki are — it's a real, permanent charge.
	tx := db.Transaction{
		AppId:        &app.ID,
		Type:         constants.TRANSACTION_TYPE_OUTGOING,
		State:        constants.TRANSACTION_STATE_SETTLED,
		AmountMloki:  100_000,
		FeeMloki:     500,
		FeeSkimMloki: 1_000,
		PaymentHash:  tests.RandomHex32(),
	}
	svc.DB.Save(&tx)

	balance := GetIsolatedBalance(svc.DB, app.ID)
	assert.Equal(t, int64(200_000-100_000-500-1_000), balance)
}

func TestGetIsolatedBalance_PendingFeeSkimReserved(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)
	app.Kind = db.AppKindCircleWallet
	svc.DB.Save(&app)

	tests.FundApp(svc, app.ID, 200_000, tests.RandomHex32())

	// A still-PENDING outgoing payment reserves its FeeSkimMloki against the
	// balance just like FeeReserveMloki does, before the real payment settles.
	tx := db.Transaction{
		AppId:           &app.ID,
		Type:            constants.TRANSACTION_TYPE_OUTGOING,
		State:           constants.TRANSACTION_STATE_PENDING,
		AmountMloki:     100_000,
		FeeReserveMloki: 10_000,
		FeeSkimMloki:    1_000,
		PaymentHash:     tests.RandomHex32(),
	}
	svc.DB.Save(&tx)

	balance := GetIsolatedBalance(svc.DB, app.ID)
	assert.Equal(t, int64(200_000-100_000-10_000-1_000), balance)
}
