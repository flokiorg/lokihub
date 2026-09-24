package apps_test

import (
	"testing"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SpentRetentionSecs governs how long a Hub keeps answering cash_status about a
// bill it destroyed. 0 is meaningful here — it disables the tombstone, i.e. go
// silent the moment a bill is gone, which is the behaviour that predates the
// field — so it must be accepted, not treated as "unset".

func TestCreateCashHub_SpentRetention_ZeroIsValid(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub, _, err := svc.AppsService.CreateCashHub(
		"test hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_HUB_SCOPE, constants.GET_BALANCE_SCOPE}, nil,
		db.CashHubConfig{PerWalletMaxMloki: 10_000, MaxExpSecs: 3600, SpentRetentionSecs: 0},
	)
	require.NoError(t, err)

	cfg, err := svc.AppsService.GetCashHubConfig(hub.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, cfg.SpentRetentionSecs, "0 means the tombstone is disabled, not unset")
}

func TestCreateCashHub_SpentRetention_Negative_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, _, err = svc.AppsService.CreateCashHub(
		"test hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_HUB_SCOPE, constants.GET_BALANCE_SCOPE}, nil,
		db.CashHubConfig{PerWalletMaxMloki: 10_000, MaxExpSecs: 3600, SpentRetentionSecs: -1},
	)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}

// A window large enough to overflow time.Duration's nanosecond range when
// converted is rejected at the boundary, exactly as MaxExpSecs is.
func TestCreateCashHub_SpentRetention_TooLarge_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, _, err = svc.AppsService.CreateCashHub(
		"test hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_HUB_SCOPE, constants.GET_BALANCE_SCOPE}, nil,
		db.CashHubConfig{
			PerWalletMaxMloki:  10_000,
			MaxExpSecs:         3600,
			SpentRetentionSecs: constants.MAX_EXPIRY_SECS + 1,
		},
	)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestUpdateCashHubConfig_SpentRetention_SetAndRead(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCashHub(t, svc, 10_000, 3600)

	retention := 7 * 24 * 60 * 60
	require.NoError(t, svc.AppsService.UpdateCashHubConfig(hub.ID, nil, nil, nil, nil, &retention))

	cfg, err := svc.AppsService.GetCashHubConfig(hub.ID)
	require.NoError(t, err)
	assert.Equal(t, retention, cfg.SpentRetentionSecs)
	// A nil pointer must leave the neighbouring fields alone.
	assert.Equal(t, 10_000, cfg.PerWalletMaxMloki)
	assert.Equal(t, 3600, cfg.MaxExpSecs)
}

// An operator must be able to turn the tombstone off again after enabling it,
// not merely shorten it.
func TestUpdateCashHubConfig_SpentRetention_ZeroDisables(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCashHub(t, svc, 10_000, 3600)
	initial := 3600
	require.NoError(t, svc.AppsService.UpdateCashHubConfig(hub.ID, nil, nil, nil, nil, &initial))

	zero := 0
	require.NoError(t, svc.AppsService.UpdateCashHubConfig(hub.ID, nil, nil, nil, nil, &zero))

	cfg, err := svc.AppsService.GetCashHubConfig(hub.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, cfg.SpentRetentionSecs)
}

func TestUpdateCashHubConfig_SpentRetention_Negative_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCashHub(t, svc, 10_000, 3600)
	negative := -1
	err = svc.AppsService.UpdateCashHubConfig(hub.ID, nil, nil, nil, nil, &negative)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}
