package nip47

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipCO"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashorder"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// chainLN is the test node with a wallet whose outputs the test sets.
// err fails every listing; tip, when set, is the chain height GetInfo reports.
type chainLN struct {
	*tests.MockLn
	outputs []lnclient.OnchainOutput
	err     error
	tip     uint32
}

func (c *chainLN) ListOnchainOutputs(context.Context) ([]lnclient.OnchainOutput, error) {
	if c.err != nil {
		return nil, c.err
	}
	return append([]lnclient.OnchainOutput(nil), c.outputs...), nil
}

func (c *chainLN) GetInfo(ctx context.Context) (*lnclient.NodeInfo, error) {
	info, err := c.MockLn.GetInfo(ctx)
	if err == nil && c.tip > 0 {
		info.BlockHeight = c.tip
	}
	return info, err
}

type intakeFixture struct {
	*orderFixture
	chain  *chainLN
	engine *cashorder.Engine
	clock  time.Time
}

// pastPayBy moves the fixture clock one second past the order's pay_by. The
// node stamps pay_by when the order is placed, a little after the fixture's
// clock started, so a fixed jump from the start can fall short of it.
func (f *intakeFixture) pastPayBy(t *testing.T, requestID string) {
	t.Helper()
	f.clock = time.Unix(f.orderRow(t, requestID).PayBy+1, 0)
}

func newIntakeFixture(t *testing.T) *intakeFixture {
	f := &intakeFixture{orderFixture: newOrderFixture(t), clock: time.Now()}
	// The node stamps order invoices with the engine's clock, so pay_by moves
	// with it.
	f.invoiceNow = func() time.Time { return f.clock }
	f.chain = &chainLN{MockLn: f.ln}
	f.engine = &cashorder.Engine{DB: f.svc.DB, Keys: f.svc.Keys, Apps: f.svc.AppsService,
		Transactions: transactions.NewTransactionsService(f.svc.DB, f.svc.EventPublisher),
		LN:           func() lnclient.LNClient { return f.chain }, Relays: func() []string { return []string{"wss://relay.example.com"} },
		Horizon: func() int64 { return 300 }, Now: func() time.Time { return f.clock }}
	return f
}

func (f *intakeFixture) orderRow(t *testing.T, requestID string) db.CashOrder {
	t.Helper()
	var o db.CashOrder
	require.NoError(t, f.svc.DB.Where("request_id = ?", requestID).First(&o).Error)
	return o
}

func (f *intakeFixture) settle(t *testing.T, o db.CashOrder, paid int64) bool {
	t.Helper()
	settled := f.clock.Unix()
	return f.engine.IntakeLightning(context.TODO(), &lnclient.Transaction{PaymentHash: *o.PaymentHash, Amount: o.PayMillis,
		AmountPaid: paid, SettledAt: &settled})
}

func (f *intakeFixture) lookupOrder(t *testing.T, buyer string, p nipCO.CashOrderParams) *nipCO.Order {
	t.Helper()
	res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashLookupParams{Credential: nipcash.BySigning(buyer),
			Request: nipcash.CashLookupRequest{RequestID: p.RequestID, Deadline: time.Now().Unix()}}.Item("l", f.conn.WalletPubkey, f.secret, b)
	})
	require.True(t, served)
	require.Nil(t, res.Error, "%+v", res.Error)
	var answer nipcash.CashLookupResult
	require.NoError(t, json.Unmarshal(res.Result, &answer))
	require.Equal(t, nipcash.LookupDone, answer.Status)
	got, err := p.ParseLookupResult(answer.Result)
	require.NoError(t, err)
	return got
}

// U-22.1: a paid Lightning order is minted once into an ordered bill that never
// expires, funded by the payment (the Hub keeps the fee); the order is minted,
// its reservation released; the lookup carries the bill; a replayed settlement
// mints nothing more.
func TestOrderIntake_LightningMintsOnce(t *testing.T) {
	f := newIntakeFixture(t)
	res, buyer, p := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	o := f.orderRow(t, p.RequestID)
	hubBefore := queries.GetIsolatedBalance(f.svc.DB, f.hub.ID)

	require.True(t, f.settle(t, o, o.PayMillis))
	require.True(t, f.settle(t, o, o.PayMillis), "a replay is an order invoice too")

	o = f.orderRow(t, p.RequestID)
	assert.Equal(t, db.CashOrderMinted, o.State)
	assert.Zero(t, o.ReservedMillis)
	var payments []db.CashOrderPayment
	require.NoError(t, f.svc.DB.Find(&payments).Error)
	require.Len(t, payments, 1)
	assert.Equal(t, db.CashOrderPaymentMinted, payments[0].State)
	assert.Equal(t, hubBefore+o.FeeMillis, queries.GetIsolatedBalance(f.svc.DB, f.hub.ID), "the Hub keeps the fee, its own balance untouched")

	var claims []db.CashWalletClaim
	require.NoError(t, f.svc.DB.Find(&claims).Error)
	require.Len(t, claims, 1)
	assert.True(t, claims[0].Ordered)
	assert.Nil(t, claims[0].UnfinalPaymentIDs, "a Lightning payment is final once settled")
	assert.Equal(t, int64(21_000), claims[0].AmountMloki)
	assert.Equal(t, 5000, claims[0].RedeemFeePpm, "the bill terms frozen at placement")
	var bill db.App
	require.NoError(t, f.svc.DB.First(&bill, claims[0].WalletAppID).Error)
	assert.Nil(t, bill.ExpiresAt, "an ordered bill never expires")
	assert.Equal(t, int64(21_000), queries.GetIsolatedBalance(f.svc.DB, bill.ID))

	got := f.lookupOrder(t, buyer, p)
	assert.Equal(t, nipCO.OrderMinted, got.State)
	require.Len(t, got.Bills, 1)
	assert.Equal(t, bill.CashToken, got.Bills[0].Token.Token, "rebuilt byte for byte")
	assert.EqualValues(t, o.PayMillis, got.Bills[0].PaidMillis)
	assert.Empty(t, got.Bills[0].Outpoint)
}

// U-22.2: an overpayment mints more, as bills of at most max_millis, the first
// ones of exactly max_millis; a payment of at most the fee mints nothing and is
// kept by the Hub.
func TestOrderIntake_SplitAndKept(t *testing.T) {
	f := newIntakeFixture(t)
	res, _, p := f.order(t, nipCO.PaymentLightning, 1_000_000, nil)
	orderOf(t, res)
	o := f.orderRow(t, p.RequestID)
	require.True(t, f.settle(t, o, 2_500_000+o.FeeMillis))
	var amounts []int64
	require.NoError(t, f.svc.DB.Model(&db.CashWalletClaim{}).Order("id").Pluck("amount_mloki", &amounts).Error)
	assert.Equal(t, []int64{1_000_000, 1_000_000, 500_000}, amounts)

	res, _, p = f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	o = f.orderRow(t, p.RequestID)
	hubBefore := queries.GetIsolatedBalance(f.svc.DB, f.hub.ID)
	require.True(t, f.settle(t, o, o.FeeMillis))
	var kept db.CashOrderPayment
	require.NoError(t, f.svc.DB.Where("order_id = ?", o.ID).First(&kept).Error)
	assert.Equal(t, db.CashOrderPaymentKept, kept.State)
	assert.Equal(t, hubBefore+o.FeeMillis, queries.GetIsolatedBalance(f.svc.DB, f.hub.ID))
}

// U-22.3: a bill that cannot be built leaves the payment confirmed and the
// order paid; the retry job mints it once the node can sign again.
func TestOrderIntake_MintRetried(t *testing.T) {
	f := newIntakeFixture(t)
	res, _, p := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	o := f.orderRow(t, p.RequestID)
	key := f.ln.SigningKey
	f.ln.SigningKey = nil
	require.True(t, f.settle(t, o, o.PayMillis))
	o = f.orderRow(t, p.RequestID)
	assert.Equal(t, db.CashOrderPaid, o.State)
	var pay db.CashOrderPayment
	require.NoError(t, f.svc.DB.First(&pay).Error)
	assert.Equal(t, db.CashOrderPaymentConfirmed, pay.State)
	assert.Equal(t, 1, pay.MintAttempts)
	var bills int64
	require.NoError(t, f.svc.DB.Model(&db.App{}).Where("kind = ?", db.AppKindCashWallet).Count(&bills).Error)
	assert.Zero(t, bills, "rolled back whole")

	f.ln.SigningKey = key
	f.clock = f.clock.Add(time.Minute)
	f.engine.RetryMints(context.TODO())
	assert.Equal(t, db.CashOrderMinted, f.orderRow(t, p.RequestID).State)
}

// U-22.4: at pay_by an unpaid Lightning order's invoice is cancelled and the
// order expires; once pay_by and deadline + H passed it is dropped with its
// record and its used mark.
func TestOrderIntake_LightningExpiresAndIsDropped(t *testing.T) {
	f := newIntakeFixture(t)
	f.ln.MockTransaction = &lnclient.Transaction{}
	res, _, p := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	f.orderRow(t, p.RequestID)

	f.pastPayBy(t, p.RequestID)
	f.engine.RunPayBy(context.TODO())
	o := f.orderRow(t, p.RequestID)
	assert.Equal(t, db.CashOrderExpired, o.State)
	assert.Zero(t, o.ReservedMillis)
	assert.Contains(t, f.ln.CancelledInvoices, *o.PaymentHash)

	f.engine.RunRetention()
	var n int64
	require.NoError(t, f.svc.DB.Model(&db.CashOrder{}).Count(&n).Error)
	assert.Zero(t, n)
	require.NoError(t, f.svc.DB.Model(&db.CashRequestRecord{}).Where("request_id = ?", p.RequestID).Count(&n).Error)
	assert.Zero(t, n)
	require.NoError(t, f.svc.DB.Model(&db.CashUsedRequestID{}).Where("request_id = ?", p.RequestID).Count(&n).Error)
	assert.Zero(t, n, "an unpaid order's mark may go after deadline + H")
}

// U-22.5: on chain, an output is seen (paid), confirmed at its tier and minted
// with its outpoint; at pay_by the order is closed, and a later output still
// mints; an output missing from two scans before confirming is dropped; an
// output to a burnt address is kept by the Hub once confirmed.
func TestOrderIntake_Onchain(t *testing.T) {
	f := newIntakeFixture(t)
	res, buyer, p := f.order(t, nipCO.PaymentOnchain, 21_000, nil)
	placed := orderOf(t, res)
	out := lnclient.OnchainOutput{TxID: tests.RandomHex32(), Vout: 1, Address: placed.Address, AmountLoki: 43}

	f.chain.outputs = []lnclient.OnchainOutput{out}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	assert.Equal(t, db.CashOrderPaid, f.orderRow(t, p.RequestID).State, "seen in the mempool")

	out.Confirmations = 1
	f.chain.outputs = []lnclient.OnchainOutput{out}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	o := f.orderRow(t, p.RequestID)
	assert.Equal(t, db.CashOrderMinted, o.State)
	got := f.lookupOrder(t, buyer, p)
	require.Len(t, got.Bills, 1)
	assert.EqualValues(t, 21_000, got.Bills[0].AmountMillis)
	assert.Equal(t, out.TxID+":1", got.Bills[0].Outpoint)
	// The bill names its on-chain payment, for cash_withdraw's unfinal check.
	var pay db.CashOrderPayment
	require.NoError(t, f.svc.DB.Where("payment_key = ?", "oc:"+out.TxID+":1").First(&pay).Error)
	var minted db.CashWalletClaim
	require.NoError(t, f.svc.DB.Where("ordered = ?", true).Order("id").First(&minted).Error)
	require.NotNil(t, minted.UnfinalPaymentIDs)
	assert.Equal(t, fmt.Sprintf("[%d]", pay.ID), *minted.UnfinalPaymentIDs)

	f.pastPayBy(t, p.RequestID)
	f.engine.RunPayBy(context.TODO())
	assert.Equal(t, db.CashOrderClosed, f.orderRow(t, p.RequestID).State)

	later := lnclient.OnchainOutput{TxID: tests.RandomHex32(), Address: placed.Address, AmountLoki: 100, Confirmations: 1}
	f.chain.outputs = []lnclient.OnchainOutput{out, later}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	got = f.lookupOrder(t, buyer, p)
	require.Len(t, got.Bills, 2, "a closed order still mints")
	assert.EqualValues(t, 100_000-22_000, got.Bills[1].AmountMillis)
	assert.Equal(t, nipCO.OrderClosed, got.State)

	// A second order: an output that leaves the mempool is dropped.
	res, _, p2 := f.order(t, nipCO.PaymentOnchain, 21_000, nil)
	second := orderOf(t, res)
	ghost := lnclient.OnchainOutput{TxID: tests.RandomHex32(), Address: second.Address, AmountLoki: 43}
	f.chain.outputs = []lnclient.OnchainOutput{out, later, ghost}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	f.chain.outputs = []lnclient.OnchainOutput{out, later}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	var dropped db.CashOrderPayment
	require.NoError(t, f.svc.DB.Where("payment_key = ?", "oc:"+ghost.TxID+":0").First(&dropped).Error)
	assert.Equal(t, db.CashOrderPaymentDropped, dropped.State)
	_ = p2

	// A burnt address: kept, credited to the Hub.
	require.NoError(t, f.svc.DB.Create(&db.CashOrderAddress{Address: "fcrt1qburnt", HubAppID: f.hub.ID, IssuedAt: 1, Burnt: true}).Error)
	hubBefore := queries.GetIsolatedBalance(f.svc.DB, f.hub.ID)
	f.chain.outputs = []lnclient.OnchainOutput{out, later, {TxID: tests.RandomHex32(), Address: "fcrt1qburnt", AmountLoki: 7, Confirmations: 1}}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	assert.Equal(t, hubBefore+7000, queries.GetIsolatedBalance(f.svc.DB, f.hub.ID))
}

// A payment seen before pay_by that then drops out of the wallet list (an RBF,
// a double spend, a partial scan) still counts as seen: at pay_by the order is
// closed, not expired, it is kept, and a later payment to its address mints
// (NIP-CO §On-chain). closed stays closed.
func TestOrderIntake_OnchainSeenThenDroppedStaysClosed(t *testing.T) {
	f := newIntakeFixture(t)
	res, buyer, p := f.order(t, nipCO.PaymentOnchain, 21_000, nil)
	placed := orderOf(t, res)
	ghost := lnclient.OnchainOutput{TxID: tests.RandomHex32(), Address: placed.Address, AmountLoki: 43}
	f.chain.outputs = []lnclient.OnchainOutput{ghost}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	f.chain.outputs = nil
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	var dropped db.CashOrderPayment
	require.NoError(t, f.svc.DB.Where("payment_key = ?", "oc:"+ghost.TxID+":0").First(&dropped).Error)
	require.Equal(t, db.CashOrderPaymentDropped, dropped.State)

	f.pastPayBy(t, p.RequestID)
	f.engine.RunPayBy(context.TODO())
	assert.Equal(t, db.CashOrderClosed, f.orderRow(t, p.RequestID).State, "a payment was seen by pay_by")
	f.engine.RunRetention()
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	assert.Equal(t, db.CashOrderClosed, f.orderRow(t, p.RequestID).State, "closed is final, and the order is kept")

	later := lnclient.OnchainOutput{TxID: tests.RandomHex32(), Address: placed.Address, AmountLoki: 100, Confirmations: 1}
	f.chain.outputs = []lnclient.OnchainOutput{later}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	got := f.lookupOrder(t, buyer, p)
	require.Len(t, got.Bills, 1, "a later payment to the address mints")
	assert.Equal(t, nipCO.OrderClosed, got.State)
}

// A confirmed on-chain payment whose first mint failed, and which a
// reorganisation then takes back below its tier, is not minted by the retry: it
// goes back to seen until it has its confirmations again (NIP-CO §On-chain).
func TestOrderIntake_OnchainReorgedBeforeMintIsNotMinted(t *testing.T) {
	f := newIntakeFixture(t)
	res, _, p := f.order(t, nipCO.PaymentOnchain, 21_000, nil)
	placed := orderOf(t, res)
	key := f.ln.SigningKey
	f.ln.SigningKey = nil
	out := lnclient.OnchainOutput{TxID: tests.RandomHex32(), Address: placed.Address, AmountLoki: 43, Confirmations: 1}
	f.chain.outputs = []lnclient.OnchainOutput{out}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	payment := func() db.CashOrderPayment {
		var pay db.CashOrderPayment
		require.NoError(t, f.svc.DB.Where("payment_key = ?", "oc:"+out.TxID+":0").First(&pay).Error)
		return pay
	}
	require.Equal(t, db.CashOrderPaymentConfirmed, payment().State, "confirmed, its mint failed")

	// The retry reads the chain again (R2-F3): the reorganisation took the
	// output's confirmations, though no scan has seen it and the stored count
	// is still 1.
	out.Confirmations = 0
	f.chain.outputs = []lnclient.OnchainOutput{out}
	require.Equal(t, 1, payment().Confirmations)
	f.ln.SigningKey = key
	f.clock = f.clock.Add(time.Hour)
	f.engine.RetryMints(context.TODO())
	assert.Equal(t, db.CashOrderPaymentSeen, payment().State, "not minted at 0 confirmations")
	assert.Equal(t, db.CashOrderPaid, f.orderRow(t, p.RequestID).State)

	// The scan also demotes a confirmed payment it sees below its tier.
	require.NoError(t, f.svc.DB.Model(&db.CashOrderPayment{}).Where("id = ?", payment().ID).Update("state", db.CashOrderPaymentConfirmed).Error)
	out.Confirmations = 0
	f.ln.SigningKey = nil
	f.chain.outputs = []lnclient.OnchainOutput{out}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	assert.Equal(t, db.CashOrderPaymentSeen, payment().State)

	// Back at its tier, it mints.
	f.ln.SigningKey = key
	out.Confirmations = 1
	f.chain.outputs = []lnclient.OnchainOutput{out}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	assert.Equal(t, db.CashOrderPaymentMinted, payment().State)
}

// U-22.6: an unpaid on-chain order expires at pay_by and is deleted at
// UnpaidOnchainDeleteAt with its address burnt; a later payment to it is kept.
func TestOrderIntake_OnchainUnpaidDeleted(t *testing.T) {
	f := newIntakeFixture(t)
	res, _, p := f.order(t, nipCO.PaymentOnchain, 21_000, nil)
	placed := orderOf(t, res)
	f.pastPayBy(t, p.RequestID)
	f.engine.RunPayBy(context.TODO())
	assert.Equal(t, db.CashOrderExpired, f.orderRow(t, p.RequestID).State)
	f.engine.RunRetention()
	var n int64
	require.NoError(t, f.svc.DB.Model(&db.CashOrder{}).Count(&n).Error)
	assert.Zero(t, n)
	var addr db.CashOrderAddress
	require.NoError(t, f.svc.DB.Where("address = ?", placed.Address).First(&addr).Error)
	assert.True(t, addr.Burnt)

	f.chain.outputs = []lnclient.OnchainOutput{{TxID: tests.RandomHex32(), Address: placed.Address, AmountLoki: 43, Confirmations: 3}}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	var kept db.CashOrderPayment
	require.NoError(t, f.svc.DB.First(&kept).Error)
	assert.Equal(t, db.CashOrderPaymentKept, kept.State)
	assert.Nil(t, kept.OrderID)
}

// U-22.7: an open order keeps its Cash Hub; the delete check names it.
func TestOrderIntake_HubDeleteWaitsForOrders(t *testing.T) {
	f := newIntakeFixture(t)
	res, _, _ := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	check, err := apps.CashHubDeleteCheckTx(f.svc.DB, f.hub.ID, time.Now())
	require.NoError(t, err)
	require.Len(t, check.Blockers, 1)
	assert.Equal(t, apps.CashHubBlockerOrders, check.Blockers[0].Kind)
	assert.Error(t, f.svc.AppsService.DeleteApp(f.hub))
}

// U-22.R3: a same-node payment of an order invoice is taken in under the order
// lock with the payer's amount, the node invoice is cancelled and the payment
// minted; a second payment of it, or one after pay_by, is refused.
func TestOrderIntake_SameNodePayment(t *testing.T) {
	f := newIntakeFixture(t)
	res, _, p := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	o := f.orderRow(t, p.RequestID)
	f.ln.MockTransaction = &lnclient.Transaction{PaymentHash: *o.PaymentHash, Preimage: tests.RandomHex32()}

	preimage, handled, err := f.engine.SelfPayLightning(context.TODO(), *o.PaymentHash, uint64(o.PayMillis)) //nolint:gosec // an order amount, never negative
	require.NoError(t, err)
	require.True(t, handled)
	assert.Equal(t, f.ln.MockTransaction.Preimage, preimage)
	assert.Contains(t, f.ln.CancelledInvoices, *o.PaymentHash)
	assert.Equal(t, db.CashOrderMinted, f.orderRow(t, p.RequestID).State)

	_, handled, err = f.engine.SelfPayLightning(context.TODO(), *o.PaymentHash, uint64(o.PayMillis)) //nolint:gosec // an order amount, never negative
	assert.True(t, handled)
	assert.ErrorIs(t, err, cashorder.ErrOrderNotPayable)

	_, handled, _ = f.engine.SelfPayLightning(context.TODO(), tests.RandomHex32(), 1000)
	assert.False(t, handled, "a hash naming no order")

	res, _, p = f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	o = f.orderRow(t, p.RequestID)
	f.ln.MockTransaction = &lnclient.Transaction{PaymentHash: *o.PaymentHash, Preimage: tests.RandomHex32()}
	f.pastPayBy(t, p.RequestID)
	_, _, err = f.engine.SelfPayLightning(context.TODO(), *o.PaymentHash, uint64(o.PayMillis)) //nolint:gosec // an order amount, never negative
	assert.ErrorIs(t, err, cashorder.ErrOrderNotPayable, "past pay_by nothing is debited")
}

// NIP-CO §Order State: a Lightning order unpaid at pay_by is expired. A read
// past pay_by ends the window itself (the invoice cancelled, the order
// expired) rather than showing the state the last job run left.
func TestOrderIntake_ReadPastPayByExpiresTheOrder(t *testing.T) {
	f := newIntakeFixture(t)
	f.nip47svc.SetOrderPayBy(f.engine.EndWindowIfDue)
	f.ln.MockTransaction = &lnclient.Transaction{}
	res, buyer, p := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	assert.Equal(t, nipCO.OrderOpen, f.lookupOrder(t, buyer, p).State)

	f.pastPayBy(t, p.RequestID)
	got := f.lookupOrder(t, buyer, p)
	assert.Equal(t, nipCO.OrderExpired, got.State, "no RunPayBy ran: the read ended the window")
	o := f.orderRow(t, p.RequestID)
	assert.Equal(t, db.CashOrderExpired, o.State)
	assert.Contains(t, f.ln.CancelledInvoices, *o.PaymentHash)
}

// NIP-CO §On-chain intake: an output in the mempool by pay_by keeps its order
// (closed, then minted at its tier), even when no scan ran between the
// payment and pay_by: the window is ended only after a scan, and the output
// is judged by when the node's wallet first saw it.
func TestOrderIntake_OnchainSeenBeforePayByScannedAfter(t *testing.T) {
	f := newIntakeFixture(t)
	res, buyer, p := f.order(t, nipCO.PaymentOnchain, 21_000, nil)
	placed := orderOf(t, res)
	out := lnclient.OnchainOutput{TxID: tests.RandomHex32(), Vout: 0, Address: placed.Address, AmountLoki: 43,
		SeenAt: f.clock.Unix()}
	f.chain.outputs = []lnclient.OnchainOutput{out}

	f.pastPayBy(t, p.RequestID) // past pay_by, no scan yet
	f.engine.RunPayBy(context.TODO())
	assert.Equal(t, db.CashOrderClosed, f.orderRow(t, p.RequestID).State, "seen by pay_by: closed, not expired")

	out.Confirmations = 1
	f.chain.outputs = []lnclient.OnchainOutput{out}
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	got := f.lookupOrder(t, buyer, p)
	require.Len(t, got.Bills, 1, "the payment mints for its order")
}

// --- A3-LH-1: a failed chain scan never ends an on-chain window ---

var errScan = errors.New("wallet unreachable")

// onchainOrder places an on-chain order and returns its request id and address.
func (f *intakeFixture) onchainOrder(t *testing.T) (string, string, nipCO.CashOrderParams) {
	t.Helper()
	res, buyer, p := f.order(t, nipCO.PaymentOnchain, 21_000, nil)
	return buyer, orderOf(t, res).Address, p
}

// mintedBills is every ordered bill minted so far.
func (f *intakeFixture) mintedBills(t *testing.T) []db.CashWalletClaim {
	t.Helper()
	var claims []db.CashWalletClaim
	require.NoError(t, f.svc.DB.Where("ordered = ?", true).Find(&claims).Error)
	return claims
}

func (f *intakeFixture) requireMintedOnce(t *testing.T, requestID string) {
	t.Helper()
	o := f.orderRow(t, requestID)
	claims := f.mintedBills(t)
	require.Len(t, claims, 1, "exactly one bill")
	assert.Equal(t, int64(21_000), claims[0].AmountMloki)
	assert.Equal(t, o.PayMillis-o.FeeMillis, claims[0].AmountMloki, "pay_millis less the fee")
	assert.Equal(t, db.CashOrderClosed, o.State)
}

// Test 1: the scan fails at pay_by: the order stays open; the next good scan
// mints the payment the node saw before pay_by.
func TestOrderIntake_ScanFailureAtPayByKeepsOrderOpen(t *testing.T) {
	f := newIntakeFixture(t)
	_, addr, p := f.onchainOrder(t)
	out := lnclient.OnchainOutput{TxID: tests.RandomHex32(), Address: addr, AmountLoki: 43, Confirmations: 1, SeenAt: f.clock.Unix()}
	f.chain.outputs = []lnclient.OnchainOutput{out}
	f.chain.err = errScan

	f.pastPayBy(t, p.RequestID)
	f.engine.RunPayBy(context.TODO())
	assert.Equal(t, db.CashOrderOpen, f.orderRow(t, p.RequestID).State, "not expired on a failed scan")

	f.chain.err = nil
	f.clock = f.clock.Add(30 * time.Second)
	f.engine.RunPayBy(context.TODO())
	f.requireMintedOnce(t, p.RequestID)
}

// Test 2: the same through a read of the order (EndWindowIfDue).
func TestOrderIntake_ScanFailureOnReadKeepsOrderOpen(t *testing.T) {
	f := newIntakeFixture(t)
	f.nip47svc.SetOrderPayBy(f.engine.EndWindowIfDue)
	buyer, addr, p := f.onchainOrder(t)
	out := lnclient.OnchainOutput{TxID: tests.RandomHex32(), Address: addr, AmountLoki: 43, Confirmations: 1, SeenAt: f.clock.Unix()}
	f.chain.outputs = []lnclient.OnchainOutput{out}
	f.chain.err = errScan

	f.pastPayBy(t, p.RequestID)
	assert.Equal(t, nipCO.OrderOpen, f.lookupOrder(t, buyer, p).State)
	assert.Equal(t, db.CashOrderOpen, f.orderRow(t, p.RequestID).State)

	f.chain.err = nil
	got := f.lookupOrder(t, buyer, p)
	require.Len(t, got.Bills, 1)
	f.requireMintedOnce(t, p.RequestID)
}

// Test 3: no node, or a node that cannot list its wallet, ends no on-chain window.
func TestOrderIntake_NoChainAtPayByKeepsOrderOpen(t *testing.T) {
	for name, ln := range map[string]func(f *intakeFixture) lnclient.LNClient{
		"no node":   func(*intakeFixture) lnclient.LNClient { return nil },
		"no lister": func(f *intakeFixture) lnclient.LNClient { return f.ln },
	} {
		t.Run(name, func(t *testing.T) {
			f := newIntakeFixture(t)
			_, _, p := f.onchainOrder(t)
			f.engine.LN = func() lnclient.LNClient { return ln(f) }
			f.pastPayBy(t, p.RequestID)
			f.engine.RunPayBy(context.TODO())
			f.engine.EndWindowIfDue(context.TODO(), f.orderRow(t, p.RequestID).ID)
			assert.Equal(t, db.CashOrderOpen, f.orderRow(t, p.RequestID).State)
		})
	}
}

// Test 4: found late, an output is judged by the node's first-seen time:
// at or before pay_by it pays the order, after it the Hub keeps it.
func TestOrderIntake_ScanFailureSeenAtBoundary(t *testing.T) {
	for name, tc := range map[string]struct {
		offset int64
		minted bool
	}{"at pay_by": {0, true}, "before pay_by": {-1, true}, "after pay_by": {1, false}} {
		t.Run(name, func(t *testing.T) {
			f := newIntakeFixture(t)
			_, addr, p := f.onchainOrder(t)
			payBy := f.orderRow(t, p.RequestID).PayBy
			f.chain.err = errScan
			f.pastPayBy(t, p.RequestID)
			f.engine.RunPayBy(context.TODO())

			f.chain.err = nil
			f.chain.outputs = []lnclient.OnchainOutput{{TxID: tests.RandomHex32(), Address: addr, AmountLoki: 43,
				Confirmations: 1, SeenAt: payBy + tc.offset}}
			f.clock = time.Unix(payBy+60, 0)
			f.engine.RunPayBy(context.TODO())
			if tc.minted {
				f.requireMintedOnce(t, p.RequestID)
				return
			}
			assert.Empty(t, f.mintedBills(t))
			assert.Equal(t, db.CashOrderExpired, f.orderRow(t, p.RequestID).State)
			var kept db.CashOrderPayment
			require.NoError(t, f.svc.DB.First(&kept).Error)
			assert.Equal(t, db.CashOrderPaymentKept, kept.State)
			assert.Nil(t, kept.OrderID)
		})
	}
}

// Test 5: a good scan that sees nothing still expires the order at pay_by.
func TestOrderIntake_HealthyScanNothingSeenExpires(t *testing.T) {
	f := newIntakeFixture(t)
	_, _, p := f.onchainOrder(t)
	f.pastPayBy(t, p.RequestID)
	f.engine.RunPayBy(context.TODO())
	assert.Equal(t, db.CashOrderExpired, f.orderRow(t, p.RequestID).State)
}

// Test 6: a failed scan holds back only on-chain windows; a Lightning order
// past pay_by in the same run expires.
func TestOrderIntake_ScanFailureLeavesLightningAlone(t *testing.T) {
	f := newIntakeFixture(t)
	f.ln.MockTransaction = &lnclient.Transaction{}
	_, _, onchain := f.onchainOrder(t)
	res, _, lightning := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	f.chain.err = errScan
	f.clock = time.Unix(max(f.orderRow(t, onchain.RequestID).PayBy, f.orderRow(t, lightning.RequestID).PayBy)+1, 0)
	f.engine.RunPayBy(context.TODO())
	assert.Equal(t, db.CashOrderOpen, f.orderRow(t, onchain.RequestID).State)
	assert.Equal(t, db.CashOrderExpired, f.orderRow(t, lightning.RequestID).State)
}

// Test 7: a payment's seen_at is the node's first-seen time when it gives an
// earlier one, else the time of the scan.
func TestOrderIntake_PaymentSeenAtIsNodeTime(t *testing.T) {
	f := newIntakeFixture(t)
	_, addr, _ := f.onchainOrder(t)
	now := f.clock.Unix()
	outs := []lnclient.OnchainOutput{
		{TxID: tests.RandomHex32(), Address: addr, AmountLoki: 43, SeenAt: now - 10},
		{TxID: tests.RandomHex32(), Address: addr, AmountLoki: 43, SeenAt: 0},
		{TxID: tests.RandomHex32(), Address: addr, AmountLoki: 43, SeenAt: now + 100},
	}
	f.chain.outputs = outs
	require.NoError(t, f.engine.ScanChain(context.TODO()))
	want := []int64{now - 10, now, now}
	for i, o := range outs {
		var p db.CashOrderPayment
		require.NoError(t, f.svc.DB.Where("payment_key = ?", "oc:"+o.TxID+":0").First(&p).Error)
		assert.Equal(t, want[i], p.SeenAt, "output %d", i)
	}
}

// Test 8: scans failing over several runs, then one good one, mint once.
func TestOrderIntake_RepeatedScanFailuresMintOnce(t *testing.T) {
	f := newIntakeFixture(t)
	_, addr, p := f.onchainOrder(t)
	f.chain.outputs = []lnclient.OnchainOutput{{TxID: tests.RandomHex32(), Address: addr, AmountLoki: 43, Confirmations: 1, SeenAt: f.clock.Unix()}}
	f.chain.err = errScan
	f.pastPayBy(t, p.RequestID)
	for range 4 {
		f.engine.RunPayBy(context.TODO())
		require.Error(t, f.engine.ScanChain(context.TODO()))
		f.clock = f.clock.Add(30 * time.Second)
		assert.Equal(t, db.CashOrderOpen, f.orderRow(t, p.RequestID).State)
	}
	f.chain.err = nil
	for range 3 {
		f.engine.RunPayBy(context.TODO())
		require.NoError(t, f.engine.ScanChain(context.TODO()))
		f.engine.RetryMints(context.TODO())
	}
	f.requireMintedOnce(t, p.RequestID)
}

// Test 9 (G10): the pay_by job and reads racing over a failing scan expire
// nothing; racing again once it recovers mint once.
func TestOrderIntake_ConcurrentPayByWithFailingScan(t *testing.T) {
	f := newIntakeFixture(t)
	_, addr, p := f.onchainOrder(t)
	id := f.orderRow(t, p.RequestID).ID
	f.chain.outputs = []lnclient.OnchainOutput{{TxID: tests.RandomHex32(), Address: addr, AmountLoki: 43, Confirmations: 1, SeenAt: f.clock.Unix()}}
	f.chain.err = errScan
	f.pastPayBy(t, p.RequestID)
	race := func() {
		var wg sync.WaitGroup
		for i := range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if i%2 == 0 {
					f.engine.RunPayBy(context.TODO())
				} else {
					f.engine.EndWindowIfDue(context.TODO(), id)
				}
			}()
		}
		wg.Wait()
	}
	race()
	assert.Equal(t, db.CashOrderOpen, f.orderRow(t, p.RequestID).State)

	f.chain.err = nil
	race()
	f.engine.RetryMints(context.TODO())
	f.requireMintedOnce(t, p.RequestID)
}

// Test 10 (G1): an output the node reports no first-seen time for, in a block
// at or below the tip read when pay_by passed, pays its order; one in a later
// block is kept by the Hub.
func TestOrderIntake_UnknownSeenAtJudgedByPayByHeight(t *testing.T) {
	for name, tc := range map[string]struct {
		height int32
		minted bool
	}{"at the tip": {100, true}, "below the tip": {99, true}, "above the tip": {101, false}} {
		t.Run(name, func(t *testing.T) {
			f := newIntakeFixture(t)
			_, addr, p := f.onchainOrder(t)
			f.chain.err, f.chain.tip = errScan, 100
			f.pastPayBy(t, p.RequestID)
			f.engine.RunPayBy(context.TODO())
			o := f.orderRow(t, p.RequestID)
			require.NotNil(t, o.PayByHeight)
			assert.Equal(t, int64(100), *o.PayByHeight)

			f.chain.err, f.chain.tip = nil, 110
			f.chain.outputs = []lnclient.OnchainOutput{{TxID: tests.RandomHex32(), Address: addr, AmountLoki: 43,
				Confirmations: uint32(110 - tc.height + 1), BlockHeight: tc.height}} //nolint:gosec // small
			f.clock = f.clock.Add(time.Hour)
			f.engine.RunPayBy(context.TODO())
			assert.Equal(t, int64(100), *f.orderRow(t, p.RequestID).PayByHeight, "recorded once")
			if tc.minted {
				f.requireMintedOnce(t, p.RequestID)
				return
			}
			assert.Empty(t, f.mintedBills(t))
			assert.Equal(t, db.CashOrderExpired, f.orderRow(t, p.RequestID).State)
		})
	}
}

// Test 11 (G2): a scan failing long past pay_by keeps the order open and logs
// an error every run; a good scan then applies the normal rule.
func TestOrderIntake_LongScanFailureLogsAndStaysOpen(t *testing.T) {
	f := newIntakeFixture(t)
	_, _, p := f.onchainOrder(t)
	var logs bytes.Buffer
	saved := logger.Logger
	logger.Logger = zerolog.New(&logs).Level(zerolog.ErrorLevel)
	t.Cleanup(func() { logger.Logger = saved })

	f.chain.err = errScan
	f.clock = time.Unix(f.orderRow(t, p.RequestID).PayBy+f.engine.Horizon()+3600, 0)
	for range 3 {
		f.engine.RunPayBy(context.TODO())
		f.clock = f.clock.Add(30 * time.Second)
	}
	assert.Equal(t, 3, strings.Count(logs.String(), "cash_order_scan_failed_total"), logs.String())
	assert.Contains(t, logs.String(), `"orders":1`)
	o := f.orderRow(t, p.RequestID)
	assert.Equal(t, db.CashOrderOpen, o.State)
	assert.Nil(t, o.PayByHeight, "no tip read long after pay_by")

	f.chain.err = nil
	f.engine.RunPayBy(context.TODO())
	assert.Equal(t, db.CashOrderExpired, f.orderRow(t, p.RequestID).State)
}
