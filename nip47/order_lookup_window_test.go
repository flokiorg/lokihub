package nip47

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipCO"
	coclient "github.com/ohstr/nmilat/nipCO/client"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CO §Order State and §Client Behavior: past pay_by a client reads an open
// order with no bill as ended unpaid, whatever its lookup answers. A Hub that
// could not end the window (chain unreadable, invoice not cancelled or not
// read) therefore answers a retryable INTERNAL, never done/open.

var errNode = errors.New("node unreachable")

// cancelFailingLN is the intake node whose invoice cancels fail with err.
type cancelFailingLN struct {
	*chainLN
	err error
}

func (c *cancelFailingLN) CancelHoldInvoice(ctx context.Context, paymentHash string) error {
	if c.err != nil {
		return c.err
	}
	return c.chainLN.CancelHoldInvoice(ctx, paymentHash)
}

// lookupAnswer is a cash_lookup of p's order: its error, or its order.
func (f *intakeFixture) lookupAnswer(t *testing.T, buyer string, p nipCO.CashOrderParams) (*transport.ResultError, *nipCO.Order) {
	t.Helper()
	res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashLookupParams{Credential: nipcash.BySigning(buyer),
			Request: nipcash.CashLookupRequest{RequestID: p.RequestID, Deadline: time.Now().Unix()}}.Item("l", f.conn.WalletPubkey, f.secret, b)
	})
	require.True(t, served)
	if res.Error != nil {
		return res.Error, nil
	}
	var answer nipcash.CashLookupResult
	require.NoError(t, json.Unmarshal(res.Result, &answer))
	require.Equal(t, nipcash.LookupDone, answer.Status)
	got, err := p.ParseLookupResult(answer.Result)
	require.NoError(t, err)
	return nil, got
}

// requireRetry asserts a lookup refused with a retryable INTERNAL, the order
// still open in the store.
func (f *intakeFixture) requireRetry(t *testing.T, buyer string, p nipCO.CashOrderParams) {
	t.Helper()
	refusal, got := f.lookupAnswer(t, buyer, p)
	require.NotNil(t, refusal, "answered %+v past pay_by with the window not ended", got)
	assert.Equal(t, constants.ERROR_INTERNAL, refusal.Code)
	assert.Contains(t, refusal.Message, "retry")
	assert.Equal(t, db.CashOrderOpen, f.orderRow(t, p.RequestID).State)
}

func (f *intakeFixture) lightningOrder(t *testing.T) (string, nipCO.CashOrderParams) {
	t.Helper()
	f.ln.MockTransaction = &lnclient.Transaction{}
	res, buyer, p := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	return buyer, p
}

func TestOrderLookup_ChainScanFailsPastPayBy_RetryThenPaid(t *testing.T) {
	f := newIntakeFixture(t)
	f.nip47svc.SetOrderPayBy(f.engine.EndWindowIfDue)
	buyer, addr, p := f.onchainOrder(t)
	// In the node's wallet before pay_by, but the wallet cannot be read.
	f.chain.outputs = []lnclient.OnchainOutput{{TxID: tests.RandomHex32(), Address: addr, AmountLoki: 43, SeenAt: f.clock.Unix()}}
	f.chain.err = errScan
	f.pastPayBy(t, p.RequestID)
	f.requireRetry(t, buyer, p)
	f.requireRetry(t, buyer, p)

	f.chain.err = nil
	refusal, got := f.lookupAnswer(t, buyer, p)
	require.Nil(t, refusal)
	assert.Equal(t, nipCO.OrderClosed, got.State, "seen by pay_by: closed, still minting")
	assert.False(t, coclient.EndedUnpaid(got, false, f.clock.Unix()))

	f.chain.outputs[0].Confirmations = 1
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	_, got = f.lookupAnswer(t, buyer, p)
	require.Len(t, got.Bills, 1)
}

func TestOrderLookup_ChainScanFailsWithPaymentRecorded_AnswersPaid(t *testing.T) {
	f := newIntakeFixture(t)
	f.nip47svc.SetOrderPayBy(f.engine.EndWindowIfDue)
	buyer, addr, p := f.onchainOrder(t)
	f.chain.outputs = []lnclient.OnchainOutput{{TxID: tests.RandomHex32(), Address: addr, AmountLoki: 43, SeenAt: f.clock.Unix()}}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	f.chain.err = errScan
	f.pastPayBy(t, p.RequestID)

	refusal, got := f.lookupAnswer(t, buyer, p)
	require.Nil(t, refusal, "a payment recorded before pay_by is the true state")
	assert.Equal(t, nipCO.OrderPaid, got.State)
	assert.False(t, coclient.EndedUnpaid(got, false, f.clock.Unix()))
}

func TestOrderLookup_HealthyScanNothingSeen_AnswersExpired(t *testing.T) {
	f := newIntakeFixture(t)
	f.nip47svc.SetOrderPayBy(f.engine.EndWindowIfDue)
	buyer, _, p := f.onchainOrder(t)
	f.pastPayBy(t, p.RequestID)
	refusal, got := f.lookupAnswer(t, buyer, p)
	require.Nil(t, refusal)
	assert.Equal(t, nipCO.OrderExpired, got.State)
	assert.True(t, coclient.EndedUnpaid(got, false, f.clock.Unix()))
}

func TestOrderLookup_InvoiceNotCancelledPastPayBy_RetryThenExpired(t *testing.T) {
	f := newIntakeFixture(t)
	node := &cancelFailingLN{chainLN: f.chain, err: errNode}
	f.engine.LN = func() lnclient.LNClient { return node }
	f.nip47svc.SetOrderPayBy(f.engine.EndWindowIfDue)
	buyer, p := f.lightningOrder(t)
	f.pastPayBy(t, p.RequestID)
	f.requireRetry(t, buyer, p)

	node.err = nil
	refusal, got := f.lookupAnswer(t, buyer, p)
	require.Nil(t, refusal)
	assert.Equal(t, nipCO.OrderExpired, got.State)
}

func TestOrderLookup_InvoiceUnreadablePastPayBy(t *testing.T) {
	t.Run("cancel fails too: retry", func(t *testing.T) {
		f := newIntakeFixture(t)
		node := &cancelFailingLN{chainLN: f.chain, err: errNode}
		f.engine.LN = func() lnclient.LNClient { return node }
		f.nip47svc.SetOrderPayBy(f.engine.EndWindowIfDue)
		buyer, p := f.lightningOrder(t)
		f.ln.MockLookupInvoiceError = errNode
		f.pastPayBy(t, p.RequestID)
		f.requireRetry(t, buyer, p)
	})
	t.Run("cancelled: the window ended, expired", func(t *testing.T) {
		f := newIntakeFixture(t)
		f.nip47svc.SetOrderPayBy(f.engine.EndWindowIfDue)
		buyer, p := f.lightningOrder(t)
		f.ln.MockLookupInvoiceError = errNode
		f.pastPayBy(t, p.RequestID)
		refusal, got := f.lookupAnswer(t, buyer, p)
		require.Nil(t, refusal)
		assert.Equal(t, nipCO.OrderExpired, got.State)
	})
}

func TestOrderLookup_NoNodePastPayBy_Retry(t *testing.T) {
	f := newIntakeFixture(t)
	f.nip47svc.SetOrderPayBy(f.engine.EndWindowIfDue)
	buyer, p := f.lightningOrder(t)
	f.engine.LN = func() lnclient.LNClient { return nil }
	f.pastPayBy(t, p.RequestID)
	f.requireRetry(t, buyer, p)
}

func TestOrderLookup_SettledInvoicePastPayBy_AnswersMinted(t *testing.T) {
	f := newIntakeFixture(t)
	f.nip47svc.SetOrderPayBy(f.engine.EndWindowIfDue)
	buyer, p := f.lightningOrder(t)
	o := f.orderRow(t, p.RequestID)
	settled := f.clock.Unix()
	// Settled before pay_by; the settlement never reached the Hub.
	f.ln.MockTransaction = &lnclient.Transaction{PaymentHash: *o.PaymentHash, Amount: o.PayMillis, AmountPaid: o.PayMillis,
		SettledAt: &settled, Preimage: tests.RandomHex32()}
	f.pastPayBy(t, p.RequestID)
	refusal, got := f.lookupAnswer(t, buyer, p)
	require.Nil(t, refusal)
	assert.Equal(t, nipCO.OrderMinted, got.State)
	require.Len(t, got.Bills, 1)
}

func TestOrderLookup_OpenAnsweredUntilPayByOnly(t *testing.T) {
	f := newIntakeFixture(t)
	f.nip47svc.SetOrderPayBy(f.engine.EndWindowIfDue)
	buyer, _, p := f.onchainOrder(t)
	f.chain.err = errScan
	payBy := f.orderRow(t, p.RequestID).PayBy

	f.clock = time.Unix(payBy, 0)
	refusal, got := f.lookupAnswer(t, buyer, p)
	require.Nil(t, refusal, "at pay_by the order is still open")
	assert.Equal(t, nipCO.OrderOpen, got.State)

	f.clock = time.Unix(payBy+1, 0)
	f.requireRetry(t, buyer, p)
}

func TestOrderLookup_NoWindowHook_OpenPastPayByRefused(t *testing.T) {
	f := newIntakeFixture(t)
	buyer, _, p := f.onchainOrder(t)
	refusal, got := f.lookupAnswer(t, buyer, p)
	require.Nil(t, refusal)
	assert.Equal(t, nipCO.OrderOpen, got.State, "before pay_by")

	require.NoError(t, f.svc.DB.Model(&db.CashOrder{}).Where("request_id = ?", p.RequestID).
		Update("pay_by", time.Now().Unix()-1).Error)
	f.requireRetry(t, buyer, p)
}
