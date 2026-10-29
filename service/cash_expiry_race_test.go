package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/ledger"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// expiryRace is a funded live bill with one slice for its whole balance, the
// recipient holding it by pubkey, and a sweep that runs once the bill expires.
type expiryRace struct {
	svc        *tests.TestService
	hub, bill  *db.App
	slice      db.CashWalletClaim
	sliceMloki uint64
}

func newExpiryRace(t *testing.T) *expiryRace {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	svc.LNClient.(*tests.MockLn).Pubkey = selfPaymentPubkey
	hub, _, err := svc.AppsService.CreateApp("hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCashHub, nil, "", nil)
	require.NoError(t, err)
	bill := createSubWallet(t, svc, db.AppKindCashWallet, hub.ID, db.ParentKindCash, makeFutureTime())
	const funded = uint64(200_000)
	tests.FundApp(svc, bill.ID, funded, tests.RandomHex32())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(bill.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: int64(funded)},
	}))
	r := &expiryRace{svc: svc, hub: hub, bill: bill, sliceMloki: funded}
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", bill.ID).First(&r.slice).Error)
	return r
}

func (r *expiryRace) ref() apps.SliceRef {
	return apps.SliceRef{ClaimID: r.slice.ID, IdentityType: r.slice.IdentityType, IdentityValue: r.slice.IdentityValue, TransferCount: r.slice.TransferCount}
}

// expireAndSweep moves the bill past its expiry and runs the cleanup sweep.
func (r *expiryRace) expireAndSweep(t *testing.T) {
	t.Helper()
	require.NoError(t, r.svc.DB.Model(&db.App{}).Where("id = ?", r.bill.ID).Update("expires_at", makeExpiredTime()).Error)
	runCashCleanup(context.TODO(), r.svc.DB, transactions.NewTransactionsService(r.svc.DB, r.svc.EventPublisher), r.svc.LNClient, r.svc.EventPublisher)
}

func (r *expiryRace) billGone(t *testing.T) bool {
	t.Helper()
	var n int64
	require.NoError(t, r.svc.DB.Model(&db.App{}).Where("id = ?", r.bill.ID).Count(&n).Error)
	return n == 0
}

func (r *expiryRace) archivedAs(t *testing.T) string {
	t.Helper()
	var a db.CashBillSliceArchive
	require.NoError(t, r.svc.DB.Where("claim_id = ?", r.slice.ID).First(&a).Error)
	return a.Outcome
}

// G5: the expiry sweep after a live slice was claimed by cash_redeem, its
// payment not started yet: the redeem won, so the sweep waits and takes
// nothing from the bill.
func TestExpirySweepAfterRedeemClaim_RedeemWins(t *testing.T) {
	r := newExpiryRace(t)
	require.NoError(t, r.svc.AppsService.ClaimSliceForRedeem(apps.RedeemClaim{Slice: r.ref(), HubAppID: r.hub.ID,
		PaymentHash: tests.RandomHex32()}, time.Now()))
	r.expireAndSweep(t)
	assert.False(t, r.billGone(t), "the sweep defers while the redeem is in flight")
	assert.EqualValues(t, r.sliceMloki, queries.GetIsolatedBalance(r.svc.DB, r.bill.ID), "the redeem's funds stay in the bill")
}

// G5: a cash_redeem after the expiry sweep took the bill is refused as claimed.
func TestExpirySweepBeforeRedeemClaim_SweepWins(t *testing.T) {
	r := newExpiryRace(t)
	r.expireAndSweep(t)
	require.True(t, r.billGone(t))
	err := r.svc.AppsService.ClaimSliceForRedeem(apps.RedeemClaim{Slice: r.ref(), HubAppID: r.hub.ID,
		PaymentHash: tests.RandomHex32()}, time.Now())
	assert.ErrorIs(t, err, apps.ErrSliceClaimed)
	assert.Equal(t, db.CashSliceStatusExpired, r.archivedAs(t))
}

// G5: a transfer committed before the sweep keeps its value; the sweep then
// archives the slice as transferred, never expired.
func TestExpirySweepAfterTransfer_TransferWins(t *testing.T) {
	r := newExpiryRace(t)
	target := createSubWallet(t, r.svc, db.AppKindCashWallet, r.hub.ID, db.ParentKindCash, makeFutureTime())
	require.NoError(t, r.svc.DB.Transaction(func(tx *gorm.DB) error {
		if err := apps.ClaimCashSliceTx(tx, r.ref(), &target.ID, db.CashSliceStatusTransferred, time.Now()); err != nil {
			return err
		}
		return ledger.InternalTransferTx(tx, r.bill.ID, target.ID, r.sliceMloki, "cash transfer")
	}))
	r.expireAndSweep(t)
	require.True(t, r.billGone(t))
	assert.Equal(t, db.CashSliceStatusSplit, r.archivedAs(t), "the bill-death archive's word for a value moved on")
	assert.EqualValues(t, r.sliceMloki, queries.GetIsolatedBalance(r.svc.DB, target.ID), "the transfer keeps its value")
}

// G5: a transfer after the sweep took the bill is refused as claimed.
func TestExpirySweepBeforeTransfer_SweepWins(t *testing.T) {
	r := newExpiryRace(t)
	r.expireAndSweep(t)
	require.True(t, r.billGone(t))
	err := r.svc.DB.Transaction(func(tx *gorm.DB) error {
		return apps.ClaimCashSliceTx(tx, r.ref(), nil, db.CashSliceStatusTransferred, time.Now())
	})
	assert.ErrorIs(t, err, apps.ErrSliceClaimed)
	assert.Equal(t, db.CashSliceStatusExpired, r.archivedAs(t))
}

// A redeem claim whose payment never reached the ledger within
// redeemStartWindow (the process stopped in between) no longer holds the sweep.
func TestExpirySweepAfterStaleRedeemClaim_Proceeds(t *testing.T) {
	r := newExpiryRace(t)
	require.NoError(t, r.svc.AppsService.ClaimSliceForRedeem(apps.RedeemClaim{Slice: r.ref(), HubAppID: r.hub.ID,
		PaymentHash: tests.RandomHex32()}, time.Now().Add(-redeemStartWindow-time.Minute)))
	r.expireAndSweep(t)
	assert.True(t, r.billGone(t))
}
