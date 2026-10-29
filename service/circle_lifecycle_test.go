package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

type lifecycleFixture struct {
	svc *tests.TestService
	ts  transactions.TransactionsService
	hub *db.App
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	hub := &db.App{Name: "hub", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleHub}
	require.NoError(t, svc.DB.Create(hub).Error)
	_, err = tests.AddCircleHubConfig(svc, hub.ID, func(c *db.CircleHubConfig) { c.GraceSecs = 1000 })
	require.NoError(t, err)
	return &lifecycleFixture{svc: svc, ts: transactions.NewTransactionsService(svc.DB, svc.EventPublisher), hub: hub}
}

// wallet is a member wallet in state with its grace ending at graceEnds (0: none),
// dust 1000 mloki, funded with balance.
func (f *lifecycleFixture) wallet(t *testing.T, state string, expires *time.Time, graceEnds int64, balance uint64) *db.App {
	t.Helper()
	dust := int64(1000)
	w := &db.App{Name: "w", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleWallet, ParentAppID: &f.hub.ID,
		ParentKind: db.ParentKindCircle, ExpiresAt: expires, CircleState: state, CircleDustThresholdMloki: &dust}
	if graceEnds != 0 {
		start := graceEnds - 1000
		w.CircleGraceStartedAt, w.CircleGraceEndsAt = &start, &graceEnds
	}
	require.NoError(t, f.svc.DB.Create(w).Error)
	require.NoError(t, f.svc.DB.Create(&db.CircleWalletMembership{CircleHubAppID: f.hub.ID, RequesterPubkey: tests.RandomHex32(), WalletAppID: w.ID}).Error)
	require.NoError(t, f.svc.DB.Create(&db.CircleJoinRecord{CircleHubAppID: f.hub.ID, HubWalletPubkey: tests.RandomHex32(), HubConnPubkey: tests.RandomHex32(),
		AuthorizerPubkey: tests.RandomHex32(), ConnectionPubkey: w.AppPubkey, ProofCreatedAt: 1, Deadline: 301, EncryptedDetails: "x", WalletAppID: w.ID}).Error)
	if balance > 0 {
		tests.FundApp(f.svc, w.ID, balance, tests.RandomHex32())
	}
	return w
}

func (f *lifecycleFixture) run(t *testing.T, now time.Time) CircleLifecycleResult {
	t.Helper()
	return RunCircleLifecycle(context.TODO(), f.svc.DB, f.ts, f.svc.LNClient, f.svc.EventPublisher, now)
}

func (f *lifecycleFixture) state(t *testing.T, w *db.App) string {
	t.Helper()
	var stored db.App
	if err := f.svc.DB.First(&stored, w.ID).Error; err != nil {
		return "deleted"
	}
	return circlelife.State(&stored)
}

// The job moves an expired wallet to Expired and a Suspended one past its grace
// to Closing; it deletes an Expired or Closing wallet past its grace that holds
// at most its dust, keeping the dust on the Hub (U-16.7), and never one that
// holds more (U-16.2).
func TestRunCircleLifecycle_ExpiresClosesAndDeletes(t *testing.T) {
	f := newLifecycleFixture(t)
	now := time.Now()
	past := now.Add(-100 * time.Second)

	expiring := f.wallet(t, circlelife.StateActive, &past, 0, 0)
	suspendedOver := f.wallet(t, circlelife.StateSuspended, nil, now.Unix(), 5000)
	dusty := f.wallet(t, circlelife.StateClosing, nil, now.Unix()-1, 1000)
	rich := f.wallet(t, circlelife.StateExpired, nil, now.Unix()-1, 1001)
	inGrace := f.wallet(t, circlelife.StateClosing, nil, now.Unix()+60, 0)

	result := f.run(t, now)
	assert.Equal(t, circlelife.StateExpired, f.state(t, expiring))
	assert.Equal(t, circlelife.StateClosing, f.state(t, suspendedOver))
	assert.Equal(t, "deleted", f.state(t, dusty))
	assert.Equal(t, circlelife.StateExpired, f.state(t, rich), "more than dust: never deleted")
	assert.Equal(t, circlelife.StateClosing, f.state(t, inGrace), "still within its grace")
	assert.Equal(t, int64(1000), queries.GetIsolatedBalance(f.svc.DB, f.hub.ID), "the dust stays with the Hub")
	assert.Equal(t, int64(1001), queries.GetIsolatedBalance(f.svc.DB, rich.ID))
	assert.Equal(t, 1, result.Deleted)

	var record db.CircleJoinRecord
	require.NoError(t, f.svc.DB.Where("wallet_app_id = ?", dusty.ID).First(&record).Error)
	require.NotNil(t, record.WalletDeletedAt, "the join record keeps the deletion for its retention")

	var expiredRow db.App
	require.NoError(t, f.svc.DB.First(&expiredRow, expiring.ID).Error)
	require.NotNil(t, expiredRow.CircleGraceEndsAt)
	assert.Equal(t, past.Unix()+1000, *expiredRow.CircleGraceEndsAt, "the grace runs from expires_at")
}

// U-16.6: a wallet with a payment going out is not deleted; an accepted held
// payment of a wallet that is not Active is cancelled first, which ends it.
func TestRunCircleLifecycle_InFlightKeepsTheWallet(t *testing.T) {
	f := newLifecycleFixture(t)
	now := time.Now()
	sending := f.wallet(t, circlelife.StateExpired, nil, now.Unix()-1, 2000)
	require.NoError(t, f.svc.DB.Create(&db.Transaction{AppId: &sending.ID, Type: constants.TRANSACTION_TYPE_OUTGOING,
		State: constants.TRANSACTION_STATE_PENDING, AmountMloki: 1500, PaymentHash: tests.RandomHex32()}).Error)
	held := f.wallet(t, circlelife.StateClosing, nil, now.Unix()+60, 0)
	holdHash := tests.RandomHex32()
	require.NoError(t, f.svc.DB.Create(&db.Transaction{AppId: &held.ID, Type: constants.TRANSACTION_TYPE_INCOMING,
		State: constants.TRANSACTION_STATE_ACCEPTED, AmountMloki: 500, PaymentHash: holdHash, Hold: true, SelfPayment: true}).Error)

	f.run(t, now)
	assert.Equal(t, circlelife.StateExpired, f.state(t, sending), "a payment going out keeps it")
	var hold db.Transaction
	require.NoError(t, f.svc.DB.Where("payment_hash = ?", holdHash).First(&hold).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, hold.State, "a wallet that is not Active cannot receive")
	assert.Zero(t, queries.GetIsolatedBalance(f.svc.DB, held.ID))
}

// U-16.5: a wallet that left Active has its open invoices cancelled.
func TestRunCircleLifecycle_CancelsOpenInvoicesOfWalletsThatLeftActive(t *testing.T) {
	f := newLifecycleFixture(t)
	now := time.Now()
	future := now.Add(time.Hour)
	suspended := f.wallet(t, circlelife.StateSuspended, nil, now.Unix()+600, 0)
	active := f.wallet(t, circlelife.StateActive, nil, 0, 0)
	for _, w := range []*db.App{suspended, active} {
		require.NoError(t, f.svc.DB.Create(&db.Transaction{AppId: &w.ID, Type: constants.TRANSACTION_TYPE_INCOMING,
			State: constants.TRANSACTION_STATE_PENDING, AmountMloki: 500, ExpiresAt: &future, PaymentHash: tests.RandomHex32(), Hold: true}).Error)
	}
	f.run(t, now)
	var rows []db.Transaction
	require.NoError(t, f.svc.DB.Where("type = ?", constants.TRANSACTION_TYPE_INCOMING).Order("app_id").Find(&rows).Error)
	states := map[uint]string{}
	for _, r := range rows {
		states[*r.AppId] = r.State
	}
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, states[suspended.ID])
	assert.Equal(t, constants.TRANSACTION_STATE_PENDING, states[active.ID])
}

// U-16.3: a torn-down Hub stays while it has a wallet and goes after its last.
func TestRunCircleLifecycle_TornDownHubGoesAfterItsLastWallet(t *testing.T) {
	f := newLifecycleFixture(t)
	now := time.Now()
	teardown := now.Unix() - 2000
	require.NoError(t, f.svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", f.hub.ID).Update("teardown_requested_at", teardown).Error)
	tests.FundApp(f.svc, f.hub.ID, 5000, tests.RandomHex32())
	last := f.wallet(t, circlelife.StateClosing, nil, now.Unix()+60, 0)

	f.run(t, now)
	assert.NotNil(t, f.svc.AppsService.GetAppById(f.hub.ID), "a wallet is left")

	f.run(t, now.Add(61*time.Second))
	assert.Equal(t, "deleted", f.state(t, last))
	assert.Nil(t, f.svc.AppsService.GetAppById(f.hub.ID), "gone after its last wallet")
}
