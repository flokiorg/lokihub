package apps_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestCreateCircleIdentity_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	identity, err := svc.AppsService.CreateCircleIdentity("family circle", db.CirclePolicyFollowing, randomHex32())
	require.NoError(t, err)
	assert.Equal(t, "family circle", identity.Name)
	assert.Equal(t, db.CirclePolicyFollowing, identity.Policy)
}

func TestCreateCircleIdentity_InvalidPolicy_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, err = svc.AppsService.CreateCircleIdentity("bad", "followers", randomHex32())
	require.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestCreateCircleIdentity_EmptyProviderPubkey_Accepted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	identity, err := svc.AppsService.CreateCircleIdentity("allowlist circle", db.CirclePolicyAllowlist, "")
	require.NoError(t, err, "provider_pubkey is optional for allowlist-policy identities")
	assert.Empty(t, identity.ProviderPubkey)
}

func TestCreateCircleIdentity_MalformedProviderPubkey_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	for _, bad := range []string{
		"not-hex-at-all",
		"AB" + randomHex32()[2:], // uppercase, wrong case
		randomHex32()[:63],       // too short
		randomHex32() + "0",      // too long
	} {
		_, err := svc.AppsService.CreateCircleIdentity("bad-provider", db.CirclePolicyFollowing, bad)
		require.Error(t, err, "provider_pubkey %q should have been rejected", bad)
		assert.ErrorIs(t, err, constants.ErrInvalidParams)
	}
}

func TestGetCircleIdentity_NotFound(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, err = svc.AppsService.GetCircleIdentity(999999)
	require.Error(t, err)
}

func TestListCircleIdentities_ReturnsAll(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, err = svc.AppsService.CreateCircleIdentity("a", db.CirclePolicyAllowlist, "")
	require.NoError(t, err)
	_, err = svc.AppsService.CreateCircleIdentity("b", db.CirclePolicyFollowing, randomHex32())
	require.NoError(t, err)

	identities, err := svc.AppsService.ListCircleIdentities()
	require.NoError(t, err)
	assert.Len(t, identities, 2)
}

func TestDeleteCircleIdentity_Unreferenced_Succeeds(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	identity, err := svc.AppsService.CreateCircleIdentity("orphan", db.CirclePolicyAllowlist, "")
	require.NoError(t, err)

	require.NoError(t, svc.AppsService.DeleteCircleIdentity(identity.ID))
	_, err = svc.AppsService.GetCircleIdentity(identity.ID)
	require.Error(t, err)
}

func TestDeleteCircleIdentity_StillReferenced_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider, _, err := svc.AppsService.CreateCircleHub(
		"circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	cfg, err := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)

	err = svc.AppsService.DeleteCircleIdentity(cfg.CircleIdentityID)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	// Untouched.
	_, err = svc.AppsService.GetCircleIdentity(cfg.CircleIdentityID)
	require.NoError(t, err)
}
