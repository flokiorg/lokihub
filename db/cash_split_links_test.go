package db_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// Archived slices older than the window forget which bill their value moved
// into; newer ones keep it, and every other fact on the row stays.
func TestClearArchivedSplitLinks_ForgetsOnlyOldLinks(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	now := time.Now()
	target := uint(42)
	old := db.CashBillSliceArchive{WalletAppID: 1, HubAppID: 9, ClaimID: 1, IdentityType: "cash", IdentityValue: "a",
		AmountMloki: 1000, Outcome: db.CashSliceStatusSplit, ArchivedAt: now.Add(-100 * 24 * time.Hour), SpunOffToWalletAppID: &target}
	recent := db.CashBillSliceArchive{WalletAppID: 2, HubAppID: 9, ClaimID: 2, IdentityType: "cash", IdentityValue: "b",
		AmountMloki: 2000, Outcome: db.CashSliceStatusSplit, ArchivedAt: now.Add(-10 * 24 * time.Hour), SpunOffToWalletAppID: &target}
	require.NoError(t, svc.DB.Create(&old).Error)
	require.NoError(t, svc.DB.Create(&recent).Error)

	cleared, err := db.ClearArchivedSplitLinks(svc.DB, now.Add(-90*24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, cleared)

	var got db.CashBillSliceArchive
	require.NoError(t, svc.DB.First(&got, old.ID).Error)
	require.Nil(t, got.SpunOffToWalletAppID, "a link past the window is forgotten")
	require.Equal(t, db.CashSliceStatusSplit, got.Outcome, "the outcome stays")
	require.EqualValues(t, 1000, got.AmountMloki, "the amount stays")

	var kept db.CashBillSliceArchive
	require.NoError(t, svc.DB.First(&kept, recent.ID).Error)
	require.NotNil(t, kept.SpunOffToWalletAppID, "a link inside the window is kept")
	require.Equal(t, target, *kept.SpunOffToWalletAppID)
}
