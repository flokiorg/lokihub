package queries

import (
	"testing"
	"time"

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

// Spend resets at the start of each renewal period: a payment made before it
// no longer counts, one made at or after it does. "never" counts everything.
func TestGetBudgetUsageSat_ResetsAtPeriodStart(t *testing.T) {
	for _, renewal := range []string{
		constants.BUDGET_RENEWAL_DAILY, constants.BUDGET_RENEWAL_WEEKLY,
		constants.BUDGET_RENEWAL_MONTHLY, constants.BUDGET_RENEWAL_YEARLY,
		constants.BUDGET_RENEWAL_NEVER,
	} {
		t.Run(renewal, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			app, _, err := tests.CreateApp(svc)
			require.NoError(t, err)

			start := getStartOfBudget(renewal)
			spend := func(at time.Time, amountMloki uint64) {
				require.NoError(t, svc.DB.Create(&db.Transaction{
					AppId: &app.ID, Type: constants.TRANSACTION_TYPE_OUTGOING, State: constants.TRANSACTION_STATE_SETTLED,
					AmountMloki: amountMloki, PaymentHash: tests.RandomHex32(), CreatedAt: at,
				}).Error)
			}
			lastPeriod := start.Add(-time.Second)
			if renewal == constants.BUDGET_RENEWAL_NEVER {
				lastPeriod = time.Now().AddDate(-5, 0, 0)
				start = time.Now().Add(-time.Hour)
			}
			spend(lastPeriod, 50_000)
			spend(start, 4_000)
			spend(start.Add(time.Second), 3_000)

			usage := GetBudgetUsageSat(svc.DB, &db.AppPermission{AppId: app.ID, BudgetRenewal: renewal})
			if renewal == constants.BUDGET_RENEWAL_NEVER {
				assert.Equal(t, uint64(57), usage)
				return
			}
			assert.Equal(t, uint64(7), usage, "only this period's spend counts, including a payment made at its first instant")
		})
	}
}
