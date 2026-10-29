package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §The Cash Hub (CASH-18): a Hub created without a min_transfer_millis
// default gets one above zero; an explicit value, 0 included, is kept.
func TestCreateApp_CashHub_MinTransferDefaultsAboveZero(t *testing.T) {
	zero, one := int64(0), int64(1)
	for _, c := range []struct {
		name string
		set  *int64
		want int64
	}{
		{"omitted", nil, constants.DEFAULT_CASH_MIN_TRANSFER_MLOKI},
		{"explicit 0 is no floor", &zero, 0},
		{"explicit 1", &one, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			resp, err := newTestAPIForCreateApp(t, svc).CreateApp(&CreateAppRequest{
				Name:                  "Hub " + c.name,
				Kind:                  db.AppKindCashHub,
				Scopes:                []string{constants.CASH_HUB_SCOPE, constants.GET_BALANCE_SCOPE},
				CashPerWalletMaxMloki: 100_000,
				CashMaxExpSecs:        3600,
				CashMinTransferMloki:  c.set,
			})
			require.NoError(t, err)
			var cfg db.CashHubConfig
			require.NoError(t, svc.DB.Where("app_id = ?", resp.Id).First(&cfg).Error)
			assert.Equal(t, c.want, cfg.MinTransferMloki)
		})
	}
	assert.Positive(t, constants.DEFAULT_CASH_MIN_TRANSFER_MLOKI)
}
