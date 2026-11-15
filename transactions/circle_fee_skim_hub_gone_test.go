package transactions

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// The hub's fee credit must never stop the member's own payment from being
// recorded. If the hub row is gone by the time the payment settles (every
// delete path refuses while a payment is in flight, so this takes a row
// removed some other way), the payment still settles and the skim is skipped.
func TestSendPaymentSync_CircleWallet_FeeSkim_HubGoneMidPayment_PaymentStillSettles(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 10_000)
	wallet := newCircleWallet(t, svc, hub, 0, constants.BUDGET_RENEWAL_NEVER)
	tests.FundApp(svc, wallet.ID, 140_000, tests.RandomHex32())

	delay := 300 * time.Millisecond
	svc.LNClient.(*tests.MockLn).PaymentDelay = &delay

	type result struct {
		tx  *Transaction
		err error
	}
	done := make(chan result, 1)
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	go func() {
		tx, err := ts.SendPaymentSync(tests.MockInvoice, nil, nil, svc.LNClient, &wallet.ID, nil)
		done <- result{tx, err}
	}()

	require.Eventually(t, func() bool {
		var count int64
		svc.DB.Model(&db.Transaction{}).Where("app_id = ? AND type = ? AND state = ?",
			wallet.ID, constants.TRANSACTION_TYPE_OUTGOING, constants.TRANSACTION_STATE_PENDING).Count(&count)
		return count == 1
	}, 2*time.Second, 10*time.Millisecond)
	require.NoError(t, svc.DB.Delete(&db.App{}, hub.ID).Error)

	r := <-done
	require.NoError(t, r.err)
	require.NotNil(t, r.tx)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, r.tx.State)
}
