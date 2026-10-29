package transactions

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CW §Deleting a Circle Wallet: a held keysend settled at the node is a
// payment in flight to its wallet, recorded where a deletion's check sees it,
// until the commit that credits it.
func TestHeldKeysend_ReservedInFlightUntilCredited(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	ts.SetLNClientProvider(func() lnclient.LNClient { return svc.LNClient })
	child := keysendCircleChild(t, svc)
	records := []lnclient.TLVRecord{customKeyRecord(child.ID)}

	hash, preimage := tests.RandomHex32(), tests.RandomHex32()
	holdKeysend(t, ts, hash, preimage, 60_000, records)
	inFlight, err := circlelife.InFlight(svc.DB, child.ID)
	require.NoError(t, err)
	assert.True(t, inFlight, "settled at the node, not yet credited")

	ts.ConsumeEvent(context.TODO(), &events.Event{Event: "nwc_lnclient_payment_received", Properties: &lnclient.Transaction{
		Type: "incoming", Preimage: preimage, PaymentHash: hash, Amount: 60_000, SettledAt: &tests.MockTimeUnix,
		Metadata: map[string]interface{}{"tlv_records": records},
	}}, map[string]interface{}{})
	inFlight, err = circlelife.InFlight(svc.DB, child.ID)
	require.NoError(t, err)
	assert.False(t, inFlight, "credited: the reservation ended in that commit")
	var n int64
	require.NoError(t, svc.DB.Model(&db.CircleHeldKeysend{}).Count(&n).Error)
	assert.Zero(t, n)
}

// A held keysend the wallet refuses reserves nothing.
func TestHeldKeysend_RefusedReservesNothing(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	ts.SetLNClientProvider(func() lnclient.LNClient { return svc.LNClient })
	child := keysendCircleChild(t, svc) // 100_000 mloki of room
	holdKeysend(t, ts, tests.RandomHex32(), tests.RandomHex32(), 1_000_000, []lnclient.TLVRecord{customKeyRecord(child.ID)})
	inFlight, err := circlelife.InFlight(svc.DB, child.ID)
	require.NoError(t, err)
	assert.False(t, inFlight)
}

// A same-node payment's credit cancelled before it settles (its wallet
// deleted, its invoice cancelled) fails the payer and credits nothing; one
// still pending settles.
func TestInterceptSelfPayment_SettlesOnlyAPendingCredit(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	child := keysendCircleChild(t, svc)

	credit := func() db.Transaction {
		preimage := tests.RandomHex32()
		row := db.Transaction{AppId: &child.ID, Type: constants.TRANSACTION_TYPE_INCOMING, State: constants.TRANSACTION_STATE_PENDING,
			AmountMloki: 10_000, PaymentHash: tests.RandomHex32(), Preimage: &preimage, SelfPayment: true}
		require.NoError(t, svc.DB.Create(&row).Error)
		return row
	}
	cancelled := credit()
	// What a deletion's cancel does between the payer's checks and the settle.
	require.NoError(t, svc.DB.Model(&db.Transaction{}).Where("id = ?", cancelled.ID).
		Update("state", constants.TRANSACTION_STATE_FAILED).Error)
	_, err = ts.interceptSelfPayment(cancelled.PaymentHash, 10_000, svc.LNClient)
	require.Error(t, err)
	var stored db.Transaction
	require.NoError(t, svc.DB.First(&stored, cancelled.ID).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, stored.State)

	pending := credit()
	_, err = ts.interceptSelfPayment(pending.PaymentHash, 10_000, svc.LNClient)
	require.NoError(t, err)
	var settled db.Transaction
	require.NoError(t, svc.DB.First(&settled, pending.ID).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, settled.State)
}

// The race itself: the settle re-reads its credit after the deletion's
// transaction, so it never settles a credit the deletion failed.
func TestInterceptSelfPayment_CreditFailedAfterTheFirstRead(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	child := keysendCircleChild(t, svc)
	preimage := tests.RandomHex32()
	row := db.Transaction{AppId: &child.ID, Type: constants.TRANSACTION_TYPE_INCOMING, State: constants.TRANSACTION_STATE_PENDING,
		AmountMloki: 10_000, PaymentHash: tests.RandomHex32(), Preimage: &preimage, SelfPayment: true}
	require.NoError(t, svc.DB.Create(&row).Error)

	armed := true
	require.NoError(t, svc.DB.Callback().Query().After("gorm:query").Register("test:fail_credit_after_read", func(d *gorm.DB) {
		if !armed || d.Statement.Table != "transactions" {
			return
		}
		armed = false
		require.NoError(t, d.Session(&gorm.Session{NewDB: true}).Model(&db.Transaction{}).Where("id = ?", row.ID).
			Update("state", constants.TRANSACTION_STATE_FAILED).Error)
	}))
	_, err = ts.interceptSelfPayment(row.PaymentHash, 10_000, svc.LNClient)
	require.False(t, armed)
	require.Error(t, err)
	var stored db.Transaction
	require.NoError(t, svc.DB.First(&stored, row.ID).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, stored.State, "never settled after the deletion failed it")
}

// A same-node keysend's pending credit is no invoice: cancelling a wallet's
// open invoices leaves it to its payer; a real invoice is cancelled.
func TestCancelOpenCircleInvoices_LeavesASameNodeKeysendCredit(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	child := keysendCircleChild(t, svc)
	preimage := tests.RandomHex32()
	keysend := db.Transaction{AppId: &child.ID, Type: constants.TRANSACTION_TYPE_INCOMING, State: constants.TRANSACTION_STATE_PENDING,
		AmountMloki: 10_000, PaymentHash: tests.RandomHex32(), Preimage: &preimage, SelfPayment: true}
	expires := time.Now().Add(time.Hour)
	invoice := db.Transaction{AppId: &child.ID, Type: constants.TRANSACTION_TYPE_INCOMING, State: constants.TRANSACTION_STATE_PENDING,
		AmountMloki: 10_000, PaymentHash: tests.RandomHex32(), ExpiresAt: &expires}
	require.NoError(t, svc.DB.Create(&keysend).Error)
	require.NoError(t, svc.DB.Create(&invoice).Error)

	require.NoError(t, ts.CancelOpenCircleInvoices(context.TODO(), child.ID, svc.LNClient))
	var k, i db.Transaction
	require.NoError(t, svc.DB.First(&k, keysend.ID).Error)
	require.NoError(t, svc.DB.First(&i, invoice.ID).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_PENDING, k.State)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, i.State)
}

// A reservation past its lifetime is dropped by the next held keysend's check
// and counts for nothing; a live one still counts.
func TestHeldKeysend_LapsedReservationDropped(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	ts.SetLNClientProvider(func() lnclient.LNClient { return svc.LNClient })
	mock := svc.LNClient.(*tests.MockLn)
	child := keysendCircleChild(t, svc) // 100_000 mloki of room
	lapsed := tests.RandomHex32()
	require.NoError(t, svc.DB.Create(&db.CircleHeldKeysend{PaymentHash: lapsed, WalletAppID: child.ID, HubAppID: *child.ParentAppID,
		AmountMloki: 90_000, ReservedAt: time.Now().Add(-circlelife.HeldKeysendTTL - time.Second).Unix()}).Error)

	preimage := tests.RandomHex32()
	holdKeysend(t, ts, tests.RandomHex32(), preimage, 60_000, []lnclient.TLVRecord{customKeyRecord(child.ID)})
	assert.Contains(t, mock.SettleHoldPreimages(), preimage, "the lapsed reservation takes no room")
	var n int64
	require.NoError(t, svc.DB.Model(&db.CircleHeldKeysend{}).Where("payment_hash = ?", lapsed).Count(&n).Error)
	assert.Zero(t, n)

	second := tests.RandomHex32()
	holdKeysend(t, ts, second, tests.RandomHex32(), 60_000, []lnclient.TLVRecord{customKeyRecord(child.ID)})
	assert.Contains(t, mock.CancelledInvoices, second, "the live reservation still takes its room")
}
