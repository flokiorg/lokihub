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

func TestCreateCircleHub_HappyPath_Following(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, secret, err := svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyFollowing, ProviderPubkey: randomHex32()},
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

// --- MaxExpSecs / PerWalletMaxMloki required positive ---

func TestCreateCircleHub_ZeroMaxExpSecs_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, _, err = svc.AppsService.CreateCircleHub(
		"test circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{PerWalletMaxMloki: 100_000},
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
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, nil, nil, &newMax, nil))

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
	err = svc.AppsService.UpdateCircleHubConfig(provider.ID, nil, nil, &zero, nil)
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
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, nil, nil, nil, &weekly))

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
	err = svc.AppsService.UpdateCircleHubConfig(provider.ID, nil, nil, nil, &bogus)
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
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, nil, &newFeesPpm, nil, nil))

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
	err = svc.AppsService.UpdateCircleHubConfig(provider.ID, nil, &negative, nil, nil)
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
	err = svc.AppsService.UpdateCircleHubConfig(provider.ID, nil, &tooHigh, nil, nil)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	cfg, cfgErr := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, cfgErr)
	assert.Equal(t, 5_000, cfg.FeesPpm)
}
