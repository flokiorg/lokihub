package apps_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Data Model: redeem_fee_ppm is bounded at 1,000,000 (the whole
// slice), and the destroyed-bill retention by the same absolute bound as the
// expiry ceiling. A Hub MUST reject a value above either bound rather than
// clamp it, on create and on update (CASH-data-model-04/-05/-06).

func createCashHubWithFee(svc *tests.TestService, redeemFeePpm int) error {
	_, _, err := svc.AppsService.CreateCashHub(
		"test hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_HUB_SCOPE, constants.GET_BALANCE_SCOPE}, nil,
		db.CashHubConfig{PerWalletMaxMloki: 10_000, MaxExpSecs: 3600, RedeemFeePpm: redeemFeePpm},
	)
	return err
}

func TestCreateCashHub_RedeemFeePpm_Bound(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	require.NoError(t, createCashHubWithFee(svc, constants.MAX_FEES_PPM), "the whole slice is the largest valid fee")
	assert.ErrorIs(t, createCashHubWithFee(svc, constants.MAX_FEES_PPM+1), constants.ErrInvalidParams)
}

func TestUpdateCashHubConfig_RedeemFeePpm_RejectedNotClamped(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := newCashHub(t, svc, 10_000, 3600)

	atBound := constants.MAX_FEES_PPM
	require.NoError(t, svc.AppsService.UpdateCashHubConfig(hub.ID, nil, nil, nil, &atBound, nil, nil))

	over := constants.MAX_FEES_PPM + 1
	err = svc.AppsService.UpdateCashHubConfig(hub.ID, nil, nil, nil, &over, nil, nil)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	cfg, err := svc.AppsService.GetCashHubConfig(hub.ID)
	require.NoError(t, err)
	assert.Equal(t, constants.MAX_FEES_PPM, cfg.RedeemFeePpm, "a refused update leaves the stored fee as it was")
}

func TestUpdateCashHubConfig_SpentRetention_RejectedNotClamped(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := newCashHub(t, svc, 10_000, 3600)

	atBound := constants.MAX_EXPIRY_SECS
	require.NoError(t, svc.AppsService.UpdateCashHubConfig(hub.ID, nil, nil, nil, nil, nil, &atBound))

	over := constants.MAX_EXPIRY_SECS + 1
	err = svc.AppsService.UpdateCashHubConfig(hub.ID, nil, nil, nil, nil, nil, &over)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	cfg, err := svc.AppsService.GetCashHubConfig(hub.ID)
	require.NoError(t, err)
	assert.Equal(t, constants.MAX_EXPIRY_SECS, cfg.SpentRetentionSecs, "a refused update leaves the stored retention as it was")
}
