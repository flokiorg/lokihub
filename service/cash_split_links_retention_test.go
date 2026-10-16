package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// CASH_SPLIT_LINK_RETENTION_DAYS=0 keeps every link: an operator who chose to
// keep them must not have the sweep clear them all.
func TestPruneArchivedSplitLinks_ZeroKeepsForever(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	target := uint(7)
	row := db.CashBillSliceArchive{WalletAppID: 1, HubAppID: 9, ClaimID: 1, IdentityType: "cash", IdentityValue: "a",
		AmountMloki: 1000, Outcome: db.CashSliceStatusSplit, ArchivedAt: time.Now().Add(-1000 * 24 * time.Hour), SpunOffToWalletAppID: &target}
	require.NoError(t, svc.DB.Create(&row).Error)

	pruneArchivedSplitLinks(svc.DB, 0)

	var got db.CashBillSliceArchive
	require.NoError(t, svc.DB.First(&got, row.ID).Error)
	require.NotNil(t, got.SpunOffToWalletAppID)
}
