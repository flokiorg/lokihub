package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// An omitted retention must mean the default, and an explicit 0 must mean off.
//
// These two are a pointer apart and everything depends on the distinction: a
// client that predates the field sends nothing, and if that collapsed to 0
// every hub it created would silently have the tombstone switched off — the
// exact ambiguity the field is a *int to avoid. Nothing covered this path,
// which is why it is worth its own test rather than a line in a bigger one.
func TestCashSpentRetention_OmittedMeansDefaultZeroMeansOff(t *testing.T) {
	zero := 0
	explicit := 3600

	assert.Equal(t, constants.DEFAULT_CASH_SPENT_RETENTION_SECS,
		cashSpentRetentionOrDefault(nil),
		"a caller that never heard of this field must still get a working tombstone")
	assert.Equal(t, 0, cashSpentRetentionOrDefault(&zero),
		"an explicit 0 is the opt-out and must survive as 0, not become the default")
	assert.Equal(t, explicit, cashSpentRetentionOrDefault(&explicit))
}

// The same distinction, end to end through CreateApp, so it is pinned at the
// boundary an external client actually crosses rather than only at the helper.
func TestCreateApp_RetentionDefaultReachesTheStoredHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	theAPI := newTestAPI(svc)

	created, err := theAPI.CreateApp(&CreateAppRequest{
		Name:                  "legacy-client-hub",
		Scopes:                []string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE},
		Kind:                  db.AppKindCashHub,
		CashPerWalletMaxMloki: 10_000,
		CashMaxExpSecs:        3600,
		// CashSpentRetentionSecs deliberately omitted — this is the shape a
		// client written before the field existed sends.
	})
	require.NoError(t, err)

	var config db.CashHubConfig
	require.NoError(t, svc.DB.Where("app_id = ?", created.Id).First(&config).Error)
	assert.Equal(t, constants.DEFAULT_CASH_SPENT_RETENTION_SECS, config.SpentRetentionSecs,
		"a hub created without the field must retain for the default, not fall silent immediately")

	off := 0
	disabled, err := theAPI.CreateApp(&CreateAppRequest{
		Name:                   "opted-out-hub",
		Scopes:                 []string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE},
		Kind:                   db.AppKindCashHub,
		CashPerWalletMaxMloki:  10_000,
		CashMaxExpSecs:         3600,
		CashSpentRetentionSecs: &off,
	})
	require.NoError(t, err)

	var offConfig db.CashHubConfig
	require.NoError(t, svc.DB.Where("app_id = ?", disabled.Id).First(&offConfig).Error)
	assert.Zero(t, offConfig.SpentRetentionSecs,
		"an operator who explicitly asked for no retention must get none")
}
