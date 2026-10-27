package nip47

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipCO"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashorder"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// chainLN is the test node with a wallet whose outputs the test sets.
type chainLN struct {
	*tests.MockLn
	outputs []lnclient.OnchainOutput
}

func (c *chainLN) ListOnchainOutputs(context.Context) ([]lnclient.OnchainOutput, error) {
	return append([]lnclient.OnchainOutput(nil), c.outputs...), nil
}

type intakeFixture struct {
	*orderFixture
	chain  *chainLN
	engine *cashorder.Engine
	clock  time.Time
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
	o := f.orderRow(t, p.RequestID)

	f.clock = f.clock.Add(3601 * time.Second)
	f.engine.RunPayBy(context.TODO())
	o = f.orderRow(t, p.RequestID)
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
	f.engine.ScanChain(context.TODO())
	assert.Equal(t, db.CashOrderPaid, f.orderRow(t, p.RequestID).State, "seen in the mempool")

	out.Confirmations = 1
	f.chain.outputs = []lnclient.OnchainOutput{out}
	f.engine.ScanChain(context.TODO())
	o := f.orderRow(t, p.RequestID)
	assert.Equal(t, db.CashOrderMinted, o.State)
	got := f.lookupOrder(t, buyer, p)
	require.Len(t, got.Bills, 1)
	assert.EqualValues(t, 21_000, got.Bills[0].AmountMillis)
	assert.Equal(t, out.TxID+":1", got.Bills[0].Outpoint)

	f.clock = f.clock.Add(86401 * time.Second)
	f.engine.RunPayBy(context.TODO())
	assert.Equal(t, db.CashOrderClosed, f.orderRow(t, p.RequestID).State)

	later := lnclient.OnchainOutput{TxID: tests.RandomHex32(), Address: placed.Address, AmountLoki: 100, Confirmations: 1}
	f.chain.outputs = []lnclient.OnchainOutput{out, later}
	f.engine.ScanChain(context.TODO())
	got = f.lookupOrder(t, buyer, p)
	require.Len(t, got.Bills, 2, "a closed order still mints")
	assert.EqualValues(t, 100_000-22_000, got.Bills[1].AmountMillis)
	assert.Equal(t, nipCO.OrderClosed, got.State)

	// A second order: an output that leaves the mempool is dropped.
	res, _, p2 := f.order(t, nipCO.PaymentOnchain, 21_000, nil)
	second := orderOf(t, res)
	ghost := lnclient.OnchainOutput{TxID: tests.RandomHex32(), Address: second.Address, AmountLoki: 43}
	f.chain.outputs = []lnclient.OnchainOutput{out, later, ghost}
	f.engine.ScanChain(context.TODO())
	f.chain.outputs = []lnclient.OnchainOutput{out, later}
	f.engine.ScanChain(context.TODO())
	f.engine.ScanChain(context.TODO())
	var dropped db.CashOrderPayment
	require.NoError(t, f.svc.DB.Where("payment_key = ?", "oc:"+ghost.TxID+":0").First(&dropped).Error)
	assert.Equal(t, db.CashOrderPaymentDropped, dropped.State)
	_ = p2

	// A burnt address: kept, credited to the Hub.
	require.NoError(t, f.svc.DB.Create(&db.CashOrderAddress{Address: "fcrt1qburnt", HubAppID: f.hub.ID, IssuedAt: 1, Burnt: true}).Error)
	hubBefore := queries.GetIsolatedBalance(f.svc.DB, f.hub.ID)
	f.chain.outputs = []lnclient.OnchainOutput{out, later, {TxID: tests.RandomHex32(), Address: "fcrt1qburnt", AmountLoki: 7, Confirmations: 1}}
	f.engine.ScanChain(context.TODO())
	assert.Equal(t, hubBefore+7000, queries.GetIsolatedBalance(f.svc.DB, f.hub.ID))
}

// U-22.6: an unpaid on-chain order expires at pay_by and is deleted at
// UnpaidOnchainDeleteAt with its address burnt; a later payment to it is kept.
func TestOrderIntake_OnchainUnpaidDeleted(t *testing.T) {
	f := newIntakeFixture(t)
	res, _, p := f.order(t, nipCO.PaymentOnchain, 21_000, nil)
	placed := orderOf(t, res)
	f.clock = f.clock.Add(86401 * time.Second)
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
	f.engine.ScanChain(context.TODO())
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

	preimage, handled, err := f.engine.SelfPayLightning(context.TODO(), *o.PaymentHash, uint64(o.PayMillis))
	require.NoError(t, err)
	require.True(t, handled)
	assert.Equal(t, f.ln.MockTransaction.Preimage, preimage)
	assert.Contains(t, f.ln.CancelledInvoices, *o.PaymentHash)
	assert.Equal(t, db.CashOrderMinted, f.orderRow(t, p.RequestID).State)

	_, handled, err = f.engine.SelfPayLightning(context.TODO(), *o.PaymentHash, uint64(o.PayMillis))
	assert.True(t, handled)
	assert.ErrorIs(t, err, cashorder.ErrOrderNotPayable)

	_, handled, _ = f.engine.SelfPayLightning(context.TODO(), tests.RandomHex32(), 1000)
	assert.False(t, handled, "a hash naming no order")

	res, _, p = f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	o = f.orderRow(t, p.RequestID)
	f.ln.MockTransaction = &lnclient.Transaction{PaymentHash: *o.PaymentHash, Preimage: tests.RandomHex32()}
	f.clock = f.clock.Add(3601 * time.Second)
	_, _, err = f.engine.SelfPayLightning(context.TODO(), *o.PaymentHash, uint64(o.PayMillis))
	assert.ErrorIs(t, err, cashorder.ErrOrderNotPayable, "past pay_by nothing is debited")
}
