package apps_test

import (
	"strings"
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

	provider := randomHex32()
	identity, err := svc.AppsService.CreateCircleIdentity("family circle", db.CirclePolicyFollowSet, provider, "family")
	require.NoError(t, err)
	assert.Equal(t, "family circle", identity.Name)
	assert.Equal(t, db.CirclePolicyFollowSet, identity.Policy)
	assert.Equal(t, "family", identity.FollowSetD)
	assert.Equal(t, "30000:"+provider+":family", identity.FollowSetAddress())
}

// NIP-CW §Data Model: a follow_set identity is the set's address, so it needs
// a `d` identifier as well as the provider.
func TestCreateCircleIdentity_FollowSetD_Validated(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	for _, bad := range []string{"", "   ", " family", strings.Repeat("x", 65)} {
		_, err := svc.AppsService.CreateCircleIdentity("bad-d", db.CirclePolicyFollowSet, randomHex32(), bad)
		require.ErrorIs(t, err, constants.ErrInvalidParams, "d %q should have been rejected", bad)
	}

	// An allowlist identity has no set, so a d is dropped rather than stored.
	identity, err := svc.AppsService.CreateCircleIdentity("allowlist", db.CirclePolicyAllowlist, "", "family")
	require.NoError(t, err)
	assert.Empty(t, identity.FollowSetD)
}

// Two identities on one address would read the same set, and each edit of a
// set this hub publishes would overwrite the other's.
func TestCreateCircleIdentity_SameFollowSetAddress_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := randomHex32()
	_, err = svc.AppsService.CreateCircleIdentity("first", db.CirclePolicyFollowSet, provider, "family")
	require.NoError(t, err)

	_, err = svc.AppsService.CreateCircleIdentity("second", db.CirclePolicyFollowSet, provider, "family")
	require.ErrorIs(t, err, constants.ErrInvalidParams)

	// Another d from the same provider is another circle.
	_, err = svc.AppsService.CreateCircleIdentity("friends", db.CirclePolicyFollowSet, provider, "friends")
	require.NoError(t, err)
}

func TestCreateCircleIdentity_InvalidPolicy_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, err = svc.AppsService.CreateCircleIdentity("bad", "followers", randomHex32(), "family")
	require.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestCreateCircleIdentity_EmptyProviderPubkey_Accepted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	identity, err := svc.AppsService.CreateCircleIdentity("allowlist circle", db.CirclePolicyAllowlist, "", "")
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
		_, err := svc.AppsService.CreateCircleIdentity("bad-provider", db.CirclePolicyFollowSet, bad, "family")
		require.Error(t, err, "provider_pubkey %q should have been rejected", bad)
		assert.ErrorIs(t, err, constants.ErrInvalidParams)
	}
}

// NIP-CW §Data Model: a Circle Identity records the data its admission
// mechanism needs, and for follow_set that includes the provider pubkey. An
// allowlist circle still needs none.
func TestCreateCircleIdentity_FollowSetWithoutProvider_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	_, err = svc.AppsService.CreateCircleIdentity("no-provider", db.CirclePolicyFollowSet, "", "family")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	_, err = svc.AppsService.CreateCircleIdentity("allowlist", db.CirclePolicyAllowlist, "", "")
	require.NoError(t, err)
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

	_, err = svc.AppsService.CreateCircleIdentity("a", db.CirclePolicyAllowlist, "", "")
	require.NoError(t, err)
	_, err = svc.AppsService.CreateCircleIdentity("b", db.CirclePolicyFollowSet, randomHex32(), "family")
	require.NoError(t, err)

	identities, err := svc.AppsService.ListCircleIdentities()
	require.NoError(t, err)
	assert.Len(t, identities, 2)
}

func TestDeleteCircleIdentity_Unreferenced_Succeeds(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	identity, err := svc.AppsService.CreateCircleIdentity("orphan", db.CirclePolicyAllowlist, "", "")
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
