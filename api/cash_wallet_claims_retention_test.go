package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/archive"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Archival on Deletion: operators SHOULD treat a displayed token as a
// live credential for the retention window. The archived row says when that ends;
// a hub with no retention has no window, and the row says nothing.
func TestListCashWalletClaims_ArchivedRowCarriesRetentionDeadline(t *testing.T) {
	for _, tc := range []struct {
		name      string
		retention int
	}{{"retention on", 3600}, {"retention off", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			hub := tests.CreateCashHub(t, svc, 10_000, 3600)
			require.NoError(t, svc.DB.Model(&db.CashHubConfig{}).Where("app_id = ?", hub.ID).
				Update("spent_retention_secs", tc.retention).Error)
			wallet := newBareCashWallet(t, svc, hub, 10)
			require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
				{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 10_000},
			}))
			endedAt := time.Now()
			require.NoError(t, svc.DB.Transaction(func(tx *gorm.DB) error {
				return archive.ArchiveAndDeleteCashBillTx(tx, wallet, db.CashBillOutcomeExpired, 0, endedAt)
			}))

			result, _, _, err := newTestAPI(svc).ListCashWalletClaims(hub.ID, 0, 0, "")
			require.NoError(t, err)
			require.Len(t, result, 1)
			if tc.retention == 0 {
				assert.Nil(t, result[0].RetainedUntil)
				return
			}
			require.NotNil(t, result[0].RetainedUntil)
			assert.InDelta(t, endedAt.Add(time.Hour).Unix(), *result[0].RetainedUntil, 2)
		})
	}
}
