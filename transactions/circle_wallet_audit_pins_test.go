package transactions

// Behaviour of circle wallets that is correct today and must stay that way:
// siblings can't see each other's payments, the fee skim never wraps, a failed
// payment costs no skim, and a known limit for very small caps.

import (
	"context"
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

func TestCircleWallet_SiblingCannotSeeOtherSiblingsPayments(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 0)
	a := newCircleWallet(t, svc, hub, 0, constants.BUDGET_RENEWAL_NEVER)
	b := newCircleWallet(t, svc, hub, 0, constants.BUDGET_RENEWAL_NEVER)
	bHash := tests.RandomHex32()
	tests.FundApp(svc, b.ID, 50_000, bHash)

	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	_, err = ts.LookupTransaction(ctx, bHash, nil, svc.LNClient, &a.ID)
	assert.ErrorIs(t, err, NewNotFoundError(), "A can't look up B's payment")

	listed, total, err := ts.ListTransactions(ctx, 0, 0, 0, 0, false, false, nil, svc.LNClient, &a.ID, false)
	require.NoError(t, err)
	assert.Zero(t, total)
	assert.Empty(t, listed, "A's history holds none of B's payments")

	own, err := ts.LookupTransaction(ctx, bHash, nil, svc.LNClient, &b.ID)
	require.NoError(t, err)
	assert.Equal(t, b.ID, *own.AppId, "B still sees its own payment")
}

func TestCalculateFeeSkimMloki_NeverWraps(t *testing.T) {
	exact := func(amount uint64, ppm int) uint64 {
		q := new(big.Int).Mul(new(big.Int).SetUint64(amount), big.NewInt(int64(ppm)))
		return q.Div(q, big.NewInt(constants.PPM_DIVISOR)).Uint64()
	}
	// Products far past uint64 range, which a plain multiply would wrap.
	for _, amount := range []uint64{1 << 50, 1 << 60, math.MaxUint64 / 3} {
		assert.Equal(t, exact(amount, 10_000), CalculateFeeSkimMloki(amount, 10_000), "amount %d at 1%%", amount)
	}
	assert.Equal(t, uint64(math.MaxUint64), CalculateFeeSkimMloki(math.MaxUint64, 1_000_000), "100% of the largest amount is the amount")
	// A rate above 100% is never valid; it saturates instead of panicking or wrapping.
	assert.Equal(t, uint64(math.MaxUint64), CalculateFeeSkimMloki(math.MaxUint64, 2_000_000))
}

// A payment the node reports as failed costs the member nothing, and the hub
// is not credited a skim for it.
func TestSendPaymentSync_CircleWallet_FailedPayment_NoSkim(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 10_000) // 1%
	wallet := newCircleWallet(t, svc, hub, 0, constants.BUDGET_RENEWAL_NEVER)
	tests.FundApp(svc, wallet.ID, 140_000, tests.RandomHex32())

	mock := svc.LNClient.(*tests.MockLn)
	mock.PayInvoiceResponses = []*lnclient.PayInvoiceResponse{nil}
	mock.PayInvoiceErrors = []error{errors.New("no route")}

	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	_, err = ts.SendPaymentSync(tests.MockInvoice, nil, nil, svc.LNClient, &wallet.ID, nil)
	require.Error(t, err)

	// The node may still report it later; here it reports a definite failure.
	ts.ConsumeEvent(ctx, &events.Event{
		Event: "nwc_lnclient_payment_failed",
		Properties: &lnclient.PaymentFailedEventProperties{
			Transaction: &lnclient.Transaction{PaymentHash: tests.MockPaymentHash},
			Reason:      "no route",
		},
	}, map[string]interface{}{})

	var out db.Transaction
	require.NoError(t, svc.DB.Where("app_id = ? AND type = ?", wallet.ID, constants.TRANSACTION_TYPE_OUTGOING).First(&out).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, out.State)
	assert.Equal(t, int64(140_000), queries.GetIsolatedBalance(svc.DB, wallet.ID), "the member keeps everything, skim included")
	assert.Equal(t, int64(0), queries.GetIsolatedBalance(svc.DB, hub.ID), "the hub earns nothing on a failed payment")
}

// Known limit, pinned so a change is deliberate: paying out over Lightning
// reserves at least 10 loki for routing fees on top of the amount, and the
// receive cap keeps the balance at or under the wallet's cap. So a wallet
// capped at 10 loki or less can never pay out, even 1 loki. Joins accept caps
// down to 1 loki.
func TestSendPaymentSync_CircleWallet_CapBelowFeeReserve_CannotPayOut(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 0)
	wallet := newCircleWallet(t, svc, hub, 5, constants.BUDGET_RENEWAL_NEVER) // 5 loki cap
	tests.FundApp(svc, wallet.ID, 5_000, tests.RandomHex32())

	amount := uint64(1_000)
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	_, err = ts.SendPaymentSync(tests.MockZeroAmountInvoice, &amount, nil, svc.LNClient, &wallet.ID, nil)
	assert.ErrorIs(t, err, NewInsufficientBalanceError())
}
