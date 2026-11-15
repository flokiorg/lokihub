package api

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
)

func twoSliceBill(t *testing.T, svc *tests.TestService) (*db.App, *db.App, db.CashWalletClaim) {
	t.Helper()
	hub := tests.CreateCashHub(t, svc, 300_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 200)
	removed, kept := tests.RandomHex32(), tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: removed, AmountMloki: 100_000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: kept, AmountMloki: 100_000},
	}))
	tests.FundApp(svc, wallet.ID, 200_000, "fund-wallet")
	var claim db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ? AND identity_value = ?", wallet.ID, removed).First(&claim).Error)
	return hub, wallet, claim
}

// NIP-CASH §Removing a Recipient (CASH-454): the slice's amount is swept back to
// the Cash Hub in the same atomic step that removes and archives it.
func TestDeleteCashClaim_SweepsRemovesAndArchivesInOneStep(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub, wallet, claim := twoSliceBill(t, svc)
	hubBefore := queries.GetIsolatedBalance(svc.DB, hub.ID)

	require.NoError(t, newTestAPIWithService(t, svc).DeleteCashClaim(hub.ID, wallet.ID, claim.ID))

	assert.Equal(t, hubBefore+100_000, queries.GetIsolatedBalance(svc.DB, hub.ID), "swept to the Hub")
	assert.Equal(t, int64(100_000), queries.GetIsolatedBalance(svc.DB, wallet.ID), "the other slice's value stays")
	var n int64
	require.NoError(t, svc.DB.Model(&db.CashWalletClaim{}).Where("id = ?", claim.ID).Count(&n).Error)
	assert.Zero(t, n)
	var arch db.CashBillSliceArchive
	require.NoError(t, svc.DB.Where("claim_id = ?", claim.ID).First(&arch).Error)
	assert.Equal(t, db.CashSliceStatusReclaimed, arch.Outcome)
}

// A failure after the sweep, here the archive write, undoes the sweep with it:
// the slice stays live and unclaimed and no value moved, so the removal can be
// tried again.
func TestDeleteCashClaim_AFailureAfterTheSweepUndoesTheSweep(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub, wallet, claim := twoSliceBill(t, svc)
	hubBefore := queries.GetIsolatedBalance(svc.DB, hub.ID)

	failArchive := true
	require.NoError(t, svc.DB.Callback().Create().Before("gorm:create").Register("test:fail_slice_archive", func(tx *gorm.DB) {
		if failArchive && tx.Statement.Table == "cash_bill_slice_archives" {
			_ = tx.AddError(errors.New("archive write failed"))
		}
	}))
	t.Cleanup(func() { _ = svc.DB.Callback().Create().Remove("test:fail_slice_archive") })

	require.Error(t, newTestAPIWithService(t, svc).DeleteCashClaim(hub.ID, wallet.ID, claim.ID))
	var live db.CashWalletClaim
	require.NoError(t, svc.DB.First(&live, claim.ID).Error)
	assert.Nil(t, live.ClaimedAt, "still unclaimed")
	assert.Equal(t, hubBefore, queries.GetIsolatedBalance(svc.DB, hub.ID), "nothing swept")
	assert.Equal(t, int64(200_000), queries.GetIsolatedBalance(svc.DB, wallet.ID))

	failArchive = false
	require.NoError(t, newTestAPIWithService(t, svc).DeleteCashClaim(hub.ID, wallet.ID, claim.ID), "a retry removes it")
	assert.Equal(t, hubBefore+100_000, queries.GetIsolatedBalance(svc.DB, hub.ID))
}
