package transactions

// A keysend names its recipient app by ID in a custom TLV record, which anyone
// can write. A circle wallet must not be pushed past its receive cap that way.

import (
	"context"
	"encoding/hex"
	"strconv"
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

// keysendCircleChild is a circle wallet with a 100 loki (100_000 mloki) receive
// cap and no balance.
func keysendCircleChild(t *testing.T, svc *tests.TestService) *db.App {
	t.Helper()
	hub, _, err := svc.AppsService.CreateApp("keysend-hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)
	_, err = tests.AddCircleHubConfig(svc, hub.ID)
	require.NoError(t, err)
	child, _, err := svc.AppsService.CreateApp("keysend-child", "", 100, "never", nil,
		[]string{constants.MAKE_INVOICE_SCOPE, constants.PAY_INVOICE_SCOPE},
		db.AppKindCircleWallet, &hub.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)
	require.NoError(t, tests.SetCircleMaxReceive(svc, child.ID, 100_000))
	return child
}

func customKeyRecord(appID uint) lnclient.TLVRecord {
	return lnclient.TLVRecord{
		Type:  CustomKeyTlvType,
		Value: hex.EncodeToString([]byte(strconv.FormatUint(uint64(appID), 10))),
	}
}

func receiveKeysend(t *testing.T, svc *tests.TestService, ts *transactionsService, paymentHash string, amountMloki int64, records []lnclient.TLVRecord) *Transaction {
	t.Helper()
	tx := lnclient.Transaction{
		Type:        "incoming",
		Preimage:    tests.RandomHex32(),
		PaymentHash: paymentHash,
		Amount:      amountMloki,
		SettledAt:   &tests.MockTimeUnix,
		Metadata:    map[string]interface{}{"tlv_records": records},
	}
	ts.ConsumeEvent(context.TODO(), &events.Event{Event: "nwc_lnclient_payment_received", Properties: &tx}, map[string]interface{}{})
	got, err := ts.LookupTransaction(context.TODO(), paymentHash, nil, svc.LNClient, nil)
	require.NoError(t, err)
	return got
}

func TestReceiveKeysend_CircleWallet_AboveCapNotCredited(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	child := keysendCircleChild(t, svc)

	got := receiveKeysend(t, svc, ts, tests.RandomHex32(), 1_000_000, []lnclient.TLVRecord{customKeyRecord(child.ID)})
	assert.Nil(t, got.AppId, "a keysend past the cap stays with the node, not the circle wallet")
	assert.Equal(t, int64(0), queries.GetIsolatedBalance(svc.DB, child.ID))
}

func TestReceiveKeysend_CircleWallet_WithinCapCredited(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	child := keysendCircleChild(t, svc)

	got := receiveKeysend(t, svc, ts, tests.RandomHex32(), 100_000, []lnclient.TLVRecord{customKeyRecord(child.ID)})
	require.NotNil(t, got.AppId)
	assert.Equal(t, child.ID, *got.AppId)
	assert.Equal(t, int64(100_000), queries.GetIsolatedBalance(svc.DB, child.ID))

	// Full now: the next keysend has no room left.
	got = receiveKeysend(t, svc, ts, tests.RandomHex32(), 1_000, []lnclient.TLVRecord{customKeyRecord(child.ID)})
	assert.Nil(t, got.AppId)
}

// Sent from an app on the same node, the payment is refused before it leaves,
// so the sender keeps its funds.
func TestSendKeysend_SelfPaymentToFullCircleWallet_Refused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	mock := svc.LNClient.(*tests.MockLn)
	mock.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	child := keysendCircleChild(t, svc)

	sender, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	sender.Kind = db.AppKindIsolated
	require.NoError(t, svc.DB.Save(sender).Error)
	require.NoError(t, svc.DB.Create(&db.AppPermission{AppId: sender.ID, App: *sender, Scope: constants.PAY_INVOICE_SCOPE}).Error)
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId: &sender.ID, State: constants.TRANSACTION_STATE_SETTLED,
		Type: constants.TRANSACTION_TYPE_INCOMING, AmountMloki: 2_000_000,
	}).Error)
	ev := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(ev).Error)

	_, err = ts.SendKeysend(1_000_000, mock.Pubkey, []lnclient.TLVRecord{customKeyRecord(child.ID)}, "", svc.LNClient, &sender.ID, &ev.ID)
	require.Error(t, err, "the circle wallet has no room for 1_000_000 mloki")
	assert.Equal(t, int64(2_000_000), queries.GetIsolatedBalance(svc.DB, sender.ID), "the sender keeps its funds")
	assert.Equal(t, int64(0), queries.GetIsolatedBalance(svc.DB, child.ID))

	// A keysend that fits still goes through.
	_, err = ts.SendKeysend(50_000, mock.Pubkey, []lnclient.TLVRecord{customKeyRecord(child.ID)}, "", svc.LNClient, &sender.ID, &ev.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(50_000), queries.GetIsolatedBalance(svc.DB, child.ID))
}
