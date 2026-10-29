package tests

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// A redeem claim committed with no payment row after it (a restart between the
// two, or a failed write) is resolved by the orphan sweep instead of staying
// claimed and holding an in-flight slot for good.
func TestResolveOrphanRedeemClaims(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	future := time.Now().Add(time.Hour)
	wallet, _, err := svc.AppsService.CreateApp("cash-orphan", "", 0, constants.BUDGET_RENEWAL_NEVER, &future,
		[]string{constants.CASH_REDEEM_SCOPE}, db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil)
	require.NoError(t, err)

	claimedAt := time.Now().Add(-time.Hour)
	never, paid, settledRow, recent, pending := tests.RandomHex32(), tests.RandomHex32(), tests.RandomHex32(), tests.RandomHex32(), tests.RandomHex32()
	for _, h := range []string{never, paid, settledRow, recent, pending} {
		at := claimedAt
		if h == recent {
			at = time.Now()
		}
		require.NoError(t, svc.DB.Create(&db.CashWalletClaim{WalletAppID: wallet.ID, IdentityType: db.CashIdentityPubkey,
			IdentityValue: tests.RandomHex32(), AmountMloki: 1000, ClaimedAt: &at, PaymentHash: h, EndedAs: db.CashSliceStatusRedeemed}).Error)
	}
	rowPreimage := tests.RandomHex32()
	require.NoError(t, svc.DB.Create(&db.Transaction{AppId: &wallet.ID, Type: constants.TRANSACTION_TYPE_OUTGOING,
		State: constants.TRANSACTION_STATE_SETTLED, PaymentHash: settledRow, Preimage: &rowPreimage}).Error)
	require.NoError(t, svc.DB.Create(&db.Transaction{AppId: &wallet.ID, Type: constants.TRANSACTION_TYPE_OUTGOING,
		State: constants.TRANSACTION_STATE_PENDING, PaymentHash: pending}).Error)

	paidPreimage := tests.RandomHex32()
	asked := map[string]bool{}
	lookup := func(hash string) (apps.PayoutState, string, uint64) {
		asked[hash] = true
		switch hash {
		case never:
			return apps.PayoutFailed, "", 0
		case paid:
			return apps.PayoutSucceeded, paidPreimage, 3
		}
		return apps.PayoutUnknown, "", 0
	}
	settledOn, err := svc.AppsService.ResolveOrphanRedeemClaims(time.Now().Add(-10*time.Minute), lookup)
	require.NoError(t, err)
	assert.Contains(t, settledOn, wallet.ID)

	claimOf := func(hash string) db.CashWalletClaim {
		var c db.CashWalletClaim
		require.NoError(t, svc.DB.Where("wallet_app_id = ? AND payment_hash = ?", wallet.ID, hash).First(&c).Error)
		return c
	}
	var released int64
	require.NoError(t, svc.DB.Model(&db.CashWalletClaim{}).Where("wallet_app_id = ? AND claimed_at IS NULL", wallet.ID).Count(&released).Error)
	assert.EqualValues(t, 1, released, "the payout the node never started is released")
	assert.Equal(t, paidPreimage, claimOf(paid).Preimage, "a payout the node paid is recorded")
	assert.Equal(t, rowPreimage, claimOf(settledRow).Preimage, "a settled ledger row's preimage is recorded")
	assert.NotNil(t, claimOf(recent).ClaimedAt, "a claim inside the grace is left alone")
	assert.False(t, asked[recent])
	assert.Empty(t, claimOf(pending).Preimage, "a pending payment is the reconcile's")
	assert.False(t, asked[pending])
}
