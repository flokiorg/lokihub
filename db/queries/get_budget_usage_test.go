package queries

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestGetBudgetUsageSat_FeeSkimIncluded(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)

	// A circle_hub's forwarding-fee skim is a real cost to the paying
	// circle_wallet — it must count against the wallet's own spend budget,
	// exactly like the real LN routing fee (fee_mloki) already does.
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:        &app.ID,
		Type:         constants.TRANSACTION_TYPE_OUTGOING,
		State:        constants.TRANSACTION_STATE_SETTLED,
		AmountMloki:  100_000,
		FeeMloki:     500,
		FeeSkimMloki: 1_000,
		PaymentHash:  tests.RandomHex32(),
	}).Error)

	appPermission := &db.AppPermission{
		AppId:         app.ID,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
	}

	usageSat := GetBudgetUsageSat(svc.DB, appPermission)
	assert.Equal(t, uint64(101_500)/1000, usageSat)
}
