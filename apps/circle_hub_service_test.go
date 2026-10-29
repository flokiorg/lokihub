package apps_test

import (
	"testing"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateCircleHub_HappyPath_FollowSet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, secret, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyFollowSet, ProviderPubkey: randomHex32(), FollowSetD: "family"},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	assert.NotEmpty(t, secret)
	assert.Equal(t, db.AppKindCircleHub, provider.Kind)
}

func TestCreateCircleHub_HappyPath_Allowlist(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	assert.Equal(t, db.AppKindCircleHub, provider.Kind)
}

// "followers" and "both" were removed: they authorize based on the
// requester's own self-published contact list, which anyone can fabricate
// for free and so provides no real access control.
func TestCreateCircleHub_FollowersPolicy_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, _, err = svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: "followers", ProviderPubkey: randomHex32()},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestCreateCircleHub_BothPolicy_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, _, err = svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: "both", ProviderPubkey: randomHex32()},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.ErrorIs(t, err, constants.ErrInvalidParams)
}

// The critical regression test for the identity-extraction refactor: a
// CircleIdentity must outlive the circle_hub that created it.
func TestCreateCircleHub_IdentitySurvivesHubDeletion(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "durable identity", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	cfg, err := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)
	identityID := cfg.CircleIdentityID

	require.NoError(t, svc.AppsService.DeleteApp(provider))

	identity, err := svc.AppsService.GetCircleIdentity(identityID)
	require.NoError(t, err, "the identity must survive its provider's deletion")
	assert.Equal(t, "durable identity", identity.Name)
}

// Two circle_hub apps referencing the same identity concurrently — the
// confirmed sharing model — must both see the same identity, and deleting one
// must leave the other's access fully intact.
func TestCreateCircleHub_SharedIdentity_ConcurrentReuse(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	first, _, err := svc.AppsService.CreateCircleHub(
		"circle-a", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "shared identity", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	firstCfg, err := svc.AppsService.GetCircleHubConfig(first.ID)
	require.NoError(t, err)
	sharedID := firstCfg.CircleIdentityID

	second, _, err := svc.AppsService.CreateCircleHub(
		"circle-b", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{ExistingID: &sharedID},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	secondCfg, err := svc.AppsService.GetCircleHubConfig(second.ID)
	require.NoError(t, err)
	assert.Equal(t, sharedID, secondCfg.CircleIdentityID)

	require.NoError(t, svc.AppsService.DeleteApp(first))

	// The identity and the second provider's reference to it are untouched.
	identity, err := svc.AppsService.GetCircleIdentity(sharedID)
	require.NoError(t, err)
	assert.Equal(t, "shared identity", identity.Name)
	secondCfgAfter, err := svc.AppsService.GetCircleHubConfig(second.ID)
	require.NoError(t, err)
	assert.Equal(t, sharedID, secondCfgAfter.CircleIdentityID)
}

func TestCreateCircleHub_NonexistentExistingIdentity_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	bogusID := uint(999999)
	_, _, err = svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{ExistingID: &bogusID},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.Error(t, err)
}

// --- MaxExpSecs at least 0 (0 = no ceiling, NIP-CW §Data Model); PerWalletMaxMloki required positive ---

func TestCreateCircleHub_NegativeMaxExpSecs_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, _, err = svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: -1, PerWalletMaxMloki: 100_000},
	)
	require.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestCreateCircleHub_ZeroPerWalletMaxMloki_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, _, err = svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600},
	)
	require.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestCreateCircleHub_NegativePerWalletMaxMloki_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, _, err = svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: -1},
	)
	require.ErrorIs(t, err, constants.ErrInvalidParams)
}

// --- MinBudgetRenewal default + validation ---

func TestCreateCircleHub_DefaultMinBudgetRenewal(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// No MinBudgetRenewal supplied — must default to "monthly", not an
	// empty/invalid string.
	provider, _, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	cfg, err := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)
	assert.Equal(t, constants.BUDGET_RENEWAL_MONTHLY, cfg.MinBudgetRenewal)
}

func TestCreateCircleHub_InvalidMinBudgetRenewal_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, _, err = svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000, MinBudgetRenewal: "fortnightly"},
	)
	require.ErrorIs(t, err, constants.ErrInvalidParams)
}

// --- UpdateCircleHubConfig: PerWalletMaxMloki / MinBudgetRenewal ---

func TestUpdateCircleHubConfig_PerWalletMaxMloki_Success(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	newMax := 200_000
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{PerWalletMaxMloki: &newMax}))

	cfg, err := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)
	assert.Equal(t, newMax, cfg.PerWalletMaxMloki)
}

func TestUpdateCircleHubConfig_PerWalletMaxMloki_ZeroRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	zero := 0
	err = svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{PerWalletMaxMloki: &zero})
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	// Rejected update must not have partially applied.
	cfg, cfgErr := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, cfgErr)
	assert.Equal(t, 100_000, cfg.PerWalletMaxMloki)
}

func TestUpdateCircleHubConfig_MinBudgetRenewal_Success(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	weekly := constants.BUDGET_RENEWAL_WEEKLY
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{MinBudgetRenewal: &weekly}))

	cfg, err := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)
	assert.Equal(t, constants.BUDGET_RENEWAL_WEEKLY, cfg.MinBudgetRenewal)
}

func TestUpdateCircleHubConfig_MinBudgetRenewal_InvalidRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	bogus := "fortnightly"
	err = svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{MinBudgetRenewal: &bogus})
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	cfg, cfgErr := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, cfgErr)
	assert.Equal(t, constants.BUDGET_RENEWAL_MONTHLY, cfg.MinBudgetRenewal)
}

// --- FeesPpm bounds ---

func TestCreateCircleHub_NegativeFeesPpm_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, _, err = svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000, FeesPpm: -1},
	)
	require.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestCreateCircleHub_FeesPpmAboveMax_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, _, err = svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000, FeesPpm: constants.MAX_FEES_PPM + 1},
	)
	require.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestCreateCircleHub_FeesPpmAtMax_Accepted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// 1_000_000 ppm (100%) is the exact accepted ceiling, not an off-by-one
	// rejection.
	provider, _, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000, FeesPpm: constants.MAX_FEES_PPM},
	)
	require.NoError(t, err)

	cfg, err := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)
	assert.Equal(t, constants.MAX_FEES_PPM, cfg.FeesPpm)
}

func TestUpdateCircleHubConfig_FeesPpm_Success(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	newFeesPpm := 25_000
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{FeesPpm: &newFeesPpm}))

	cfg, err := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)
	assert.Equal(t, newFeesPpm, cfg.FeesPpm)
}

func TestUpdateCircleHubConfig_FeesPpm_NegativeRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000, FeesPpm: 5_000},
	)
	require.NoError(t, err)

	negative := -1
	err = svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{FeesPpm: &negative})
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	// Rejected update must not have partially applied.
	cfg, cfgErr := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, cfgErr)
	assert.Equal(t, 5_000, cfg.FeesPpm)
}

func TestUpdateCircleHubConfig_FeesPpm_AboveMaxRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000, FeesPpm: 5_000},
	)
	require.NoError(t, err)

	tooHigh := constants.MAX_FEES_PPM + 1
	err = svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{FeesPpm: &tooHigh})
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	cfg, cfgErr := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, cfgErr)
	assert.Equal(t, 5_000, cfg.FeesPpm)
}

// --- Per-wallet ceiling floor: the 10-loki fee reserve plus 1 loki ---

func TestCreateCircleHub_PerWalletMaxBelowFeeReserveFloor_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, _, err = svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 10_999},
	)
	require.ErrorIs(t, err, constants.ErrInvalidParams)

	_, _, err = svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle at floor", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 11_000},
	)
	require.NoError(t, err)
}

func TestUpdateCircleHubConfig_PerWalletMaxBelowFeeReserveFloor_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	below := 10_999
	require.ErrorIs(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{PerWalletMaxMloki: &below}), constants.ErrInvalidParams)
	atFloor := 11_000
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{PerWalletMaxMloki: &atFloor}))
}

// --- Lifetime default and receive ceiling (NIP-CW §Data Model, U-13.2) ---

func createAllowlistHub(t *testing.T, svc *tests.TestService, config db.CircleHubConfig) (*db.App, error) {
	t.Helper()
	provider, _, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		config,
	)
	return provider, err
}

func TestCreateCircleHub_LifetimeAndReceiveDefaults(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Left out: the default lifetime is the ceiling, the receive ceiling the send one.
	provider, err := createAllowlistHub(t, svc, db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000})
	require.NoError(t, err)
	cfg, err := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)
	assert.Equal(t, 3600, cfg.DefaultExpSecs)
	assert.Equal(t, int64(100_000), cfg.PerWalletMaxReceiveMloki)

	// No ceiling and no default: wallets never expire.
	provider, err = createAllowlistHub(t, svc, db.CircleHubConfig{PerWalletMaxMloki: 100_000})
	require.NoError(t, err)
	cfg, err = svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)
	assert.Zero(t, cfg.MaxExpSecs)
	assert.Zero(t, cfg.DefaultExpSecs)

	// A default above the ceiling is refused.
	_, err = createAllowlistHub(t, svc, db.CircleHubConfig{MaxExpSecs: 3600, DefaultExpSecs: 3601, PerWalletMaxMloki: 100_000})
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
	// A negative receive ceiling is refused.
	_, err = createAllowlistHub(t, svc, db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000, PerWalletMaxReceiveMloki: -1})
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestUpdateCircleHubConfig_DefaultLifetimeAgainstCeiling(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider, err := createAllowlistHub(t, svc, db.CircleHubConfig{MaxExpSecs: 3600, DefaultExpSecs: 600, PerWalletMaxMloki: 100_000})
	require.NoError(t, err)

	over, zero, lowered := 3601, 0, 300
	// The default against the stored ceiling, and the ceiling against the stored default.
	assert.ErrorIs(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{DefaultExpSecs: &over}), constants.ErrInvalidParams)
	assert.ErrorIs(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{DefaultExpSecs: &zero}), constants.ErrInvalidParams,
		"0 (never) under a non-zero ceiling")
	assert.ErrorIs(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{MaxExpSecs: &lowered}), constants.ErrInvalidParams,
		"a ceiling below the default")
	assert.ErrorIs(t, svc.AppsService.ValidateCircleHubConfigUpdate(provider.ID, apps.CircleHubConfigPatch{MaxExpSecs: &lowered}), constants.ErrInvalidParams)

	// Both together, consistent: accepted. Then no ceiling and never.
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{MaxExpSecs: &lowered, DefaultExpSecs: &lowered}))
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{MaxExpSecs: &zero, DefaultExpSecs: &zero}))
	cfg, err := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)
	assert.Zero(t, cfg.MaxExpSecs)
	assert.Zero(t, cfg.DefaultExpSecs)

	receive := int64(50_000)
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{PerWalletMaxReceiveMloki: &receive}))
	noReceive := int64(0)
	assert.ErrorIs(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{PerWalletMaxReceiveMloki: &noReceive}), constants.ErrInvalidParams)
	cfg, err = svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)
	assert.Equal(t, receive, cfg.PerWalletMaxReceiveMloki)
}

// The dust threshold is bounded by CIRCLE_MAX_DUST_MLOKI (default 10,000 mloki),
// on creation and on update: the bound is accepted, one above it refused.
func TestCircleHubConfig_DustThresholdBounded(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	create := func(dust int64) (*db.App, error) {
		app, _, err := svc.AppsService.CreateCircleHub(
			"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
			[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE}, nil,
			apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
			db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000, DustThresholdMloki: dust},
		)
		return app, err
	}
	_, err = create(constants.CIRCLE_MAX_DUST_MLOKI + 1)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
	provider, err := create(constants.CIRCLE_MAX_DUST_MLOKI)
	require.NoError(t, err)

	over := int64(constants.CIRCLE_MAX_DUST_MLOKI + 1)
	assert.ErrorIs(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{DustThresholdMloki: &over}),
		constants.ErrInvalidParams)
	zero := int64(0)
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{DustThresholdMloki: &zero}))
	cfg, err := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)
	assert.Zero(t, cfg.DustThresholdMloki)

	// A host that raises CIRCLE_MAX_DUST_MLOKI may set up to it.
	svc.Cfg.GetEnv().CircleMaxDustMloki = 100_000_000
	_, err = create(100_000_000)
	require.NoError(t, err)
	_, err = create(100_000_001)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}
