package transactions

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// holdCircle is a circle hub with one member wallet whose receive cap is
// 500_000 mloki.
func holdCircle(t *testing.T) (*tests.TestService, *transactionsService, *db.App) {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	hub := &db.App{Name: "hub", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleHub}
	require.NoError(t, svc.DB.Create(hub).Error)
	_, err = tests.AddCircleHubConfig(svc, hub.ID)
	require.NoError(t, err)
	wallet, _, err := svc.AppsService.CreateApp("member", "", 1_000, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.MAKE_INVOICE_SCOPE, constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &hub.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)
	require.NoError(t, tests.SetCircleMaxReceive(svc, wallet.ID, 500_000))
	require.NoError(t, svc.DB.First(wallet, wallet.ID).Error)
	return svc, NewTransactionsService(svc.DB, svc.EventPublisher), wallet
}

func acceptExternally(t *testing.T, ts *transactionsService, paymentHash string, amountPaid int64) {
	t.Helper()
	deadline := uint32(800_000)
	ts.ConsumeEvent(context.TODO(), &events.Event{Event: "nwc_lnclient_hold_invoice_accepted",
		Properties: &lnclient.Transaction{PaymentHash: paymentHash, AmountPaid: amountPaid, SettleDeadline: &deadline}}, nil)
}

func rowState(t *testing.T, svc *tests.TestService, id uint) db.Transaction {
	t.Helper()
	var row db.Transaction
	require.NoError(t, svc.DB.First(&row, id).Error)
	return row
}

// U-15.6: every Circle Wallet invoice is a hold invoice the Hub settles itself:
// an accepted payment for the invoice's amount is settled, and becomes a
// Lightning receipt of the current period.
func TestCircleInvoice_HubSettlesAnAcceptedPayment(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	ts.SetLNClientProvider(func() lnclient.LNClient { return svc.LNClient })

	invoice, err := ts.MakeCircleInvoice(context.TODO(), wallet, 200_000, "dinner", "", 3600, nil, svc.LNClient, nil)
	require.NoError(t, err)
	assert.True(t, invoice.Hold)
	preimage := sealedPreimage(t, svc, invoice.ID)
	calls := svc.LNClient.(*tests.MockLn).MakeHoldInvoiceCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, invoice.PaymentHash, calls[0].PaymentHash, "the Hub's own hash")

	acceptExternally(t, ts, invoice.PaymentHash, 200_000)
	settled := rowState(t, svc, invoice.ID)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, settled.State)
	assert.Contains(t, svc.LNClient.(*tests.MockLn).SettleHoldPreimages(), preimage)
	assert.Equal(t, circlemoney.KindLightning, settled.PaymentKind)
	assert.Equal(t, int64(200_000), queries.GetIsolatedBalance(svc.DB, wallet.ID))

	room, err := circlemoney.ReceiveRoom(svc.DB, wallet, time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(300_000), room, "the receipt counts on the receive cap")
}

// U-15.6: an over-payment, and a payment of an expired invoice, are cancelled.
func TestCircleInvoice_HubCancelsWhatItRefuses(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	ts.SetLNClientProvider(func() lnclient.LNClient { return svc.LNClient })

	over, err := ts.MakeCircleInvoice(context.TODO(), wallet, 100_000, "", "", 3600, nil, svc.LNClient, nil)
	require.NoError(t, err)
	acceptExternally(t, ts, over.PaymentHash, 150_000)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, rowState(t, svc, over.ID).State, "over-paid: cancelled")

	expired, err := ts.MakeCircleInvoice(context.TODO(), wallet, 100_000, "", "", 3600, nil, svc.LNClient, nil)
	require.NoError(t, err)
	past := time.Now().Add(-time.Second)
	require.NoError(t, svc.DB.Model(&db.Transaction{}).Where("id = ?", expired.ID).Update("expires_at", &past).Error)
	acceptExternally(t, ts, expired.PaymentHash, 100_000)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, rowState(t, svc, expired.ID).State, "expired: cancelled")

	assert.Zero(t, queries.GetIsolatedBalance(svc.DB, wallet.ID), "nothing credited")
}

// A same-node payment of a Circle Wallet's invoice is accepted, re-checked and
// settled at once, and classified from the payer's side (kind 3 from another
// app on the node).
func TestCircleInvoice_SameNodePaymentSettledAtOnce(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	invoice, err := ts.MakeCircleInvoice(context.TODO(), wallet, 50_000, "", "", 3600, nil, svc.LNClient, nil)
	require.NoError(t, err)

	payer, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Create(&db.Transaction{AppId: &payer.ID, Type: constants.TRANSACTION_TYPE_OUTGOING,
		State: constants.TRANSACTION_STATE_PENDING, AmountMloki: 50_000, PaymentHash: invoice.PaymentHash, SelfPayment: true}).Error)

	preimage := sealedPreimage(t, svc, invoice.ID)
	resp, err := ts.interceptSelfPayment(invoice.PaymentHash, 50_000, svc.LNClient)
	require.NoError(t, err)
	assert.Equal(t, preimage, resp.Preimage)
	settled := rowState(t, svc, invoice.ID)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, settled.State)
	assert.Equal(t, circlemoney.KindNode, settled.PaymentKind)
	assert.Empty(t, svc.LNClient.(*tests.MockLn).SettleHoldPreimages(), "no node involved")
}

// Two payers of one circle invoice: the second read the row PENDING, but the
// first accepted (and here settled) it before the second's accept. The second
// must fail rather than take the first's settlement as its own, or it would be
// debited with no wallet credited.
func TestCircleInvoice_SecondSameNodePayerFails(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	ts.SetLNClientProvider(func() lnclient.LNClient { return svc.LNClient })
	invoice, err := ts.MakeCircleInvoice(context.TODO(), wallet, 50_000, "", "", 3600, nil, svc.LNClient, nil)
	require.NoError(t, err)
	stale := rowState(t, svc, invoice.ID)
	require.Equal(t, constants.TRANSACTION_STATE_PENDING, stale.State)

	acceptExternally(t, ts, invoice.PaymentHash, 50_000)
	require.Equal(t, constants.TRANSACTION_STATE_SETTLED, rowState(t, svc, invoice.ID).State)

	_, err = ts.interceptSelfCircleHold(&stale, svc.LNClient)
	require.Error(t, err)
	assert.Equal(t, int64(50_000), queries.GetIsolatedBalance(svc.DB, wallet.ID), "credited once")
}

// The receive room and the open-invoice cap bound invoices (QUOTA_EXCEEDED,
// RATE_LIMITED), and the holdings ceiling (INSUFFICIENT_CAPACITY).
func TestMakeCircleInvoice_Refusals(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	_, err := ts.MakeCircleInvoice(context.TODO(), wallet, 500_001, "", "", 3600, nil, svc.LNClient, nil)
	assertCircleRefusal(t, err, constants.ERROR_QUOTA_EXCEEDED)
	_, err = ts.MakeCircleInvoice(context.TODO(), wallet, 0, "", "", 3600, nil, svc.LNClient, nil)
	assertCircleRefusal(t, err, constants.ERROR_RESTRICTED)

	require.NoError(t, svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", *wallet.ParentAppID).
		Update("holdings_ceiling_mloki", 100_000).Error)
	_, err = ts.MakeCircleInvoice(context.TODO(), wallet, 100_001, "", "", 3600, nil, svc.LNClient, nil)
	assertCircleRefusal(t, err, "INSUFFICIENT_CAPACITY")
	assert.Empty(t, svc.LNClient.(*tests.MockLn).MakeHoldInvoiceCalls(), "nothing issued at the node")

	require.NoError(t, svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", *wallet.ParentAppID).
		Update("holdings_ceiling_mloki", 100_000_000).Error)
	require.NoError(t, tests.SetCircleMaxReceive(svc, wallet.ID, 100_000_000))
	require.NoError(t, svc.DB.First(wallet, wallet.ID).Error)
	future := time.Now().Add(time.Hour)
	for range circlemoney.MaxOpenInvoices {
		require.NoError(t, svc.DB.Create(&db.Transaction{AppId: &wallet.ID, Type: constants.TRANSACTION_TYPE_INCOMING,
			State: constants.TRANSACTION_STATE_PENDING, AmountMloki: 1, ExpiresAt: &future, PaymentHash: tests.RandomHex32()}).Error)
	}
	_, err = ts.MakeCircleInvoice(context.TODO(), wallet, 1_000, "", "", 3600, nil, svc.LNClient, nil)
	assertCircleRefusal(t, err, constants.ERROR_RATE_LIMITED)
}

// A wallet that left Active issues no invoice, and a payment it accepted is
// cancelled, not settled (NIP-CW §Own Funds and Caps: only Active wallets
// receive).
func TestCircleInvoice_NotActive(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	ts.SetLNClientProvider(func() lnclient.LNClient { return svc.LNClient })
	invoice, err := ts.MakeCircleInvoice(context.TODO(), wallet, 10_000, "", "", 3600, nil, svc.LNClient, nil)
	require.NoError(t, err)

	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Update("circle_state", "suspended").Error)
	_, err = ts.MakeCircleInvoice(context.TODO(), wallet, 10_000, "", "", 3600, nil, svc.LNClient, nil)
	assertCircleRefusal(t, err, constants.ERROR_RESTRICTED)

	acceptExternally(t, ts, invoice.PaymentHash, 10_000)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, rowState(t, svc, invoice.ID).State)
	assert.Zero(t, queries.GetIsolatedBalance(svc.DB, wallet.ID))
}

// The sweep of accepted payments (as after a restart) checks the amount the
// node holds like a live accept does: an over-payment is cancelled, never
// settled with the excess left to the host.
func TestCircleInvoice_SweepCancelsAnOverPayment(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	invoice, err := ts.MakeCircleInvoice(context.TODO(), wallet, 100_000, "", "", 3600, nil, svc.LNClient, nil)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(&db.Transaction{}).Where("id = ?", invoice.ID).
		Update("state", constants.TRANSACTION_STATE_ACCEPTED).Error)
	mock := svc.LNClient.(*tests.MockLn)
	mock.MockTransaction = &lnclient.Transaction{PaymentHash: invoice.PaymentHash, Amount: 100_000, AmountPaid: 150_000}

	ts.ResolveAcceptedCircleHolds(context.TODO(), svc.LNClient)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, rowState(t, svc, invoice.ID).State)
	assert.Empty(t, mock.SettleHoldPreimages())
	assert.Zero(t, queries.GetIsolatedBalance(svc.DB, wallet.ID))
}
