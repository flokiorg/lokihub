package api

// Covers the Frozen flag on ListCircleChildrenBalances: a member who left the
// circle has a frozen wallet (see nip47's circle wallet freeze), and the list
// says so without ever fetching from relays.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/service"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/tests/mocks"
)

// joinedCircleChild is a circle wallet with its membership row, as a join
// leaves it.
func joinedCircleChild(t *testing.T, svc *tests.TestService, hub *db.App, member string) *db.App {
	t.Helper()
	child := createCircleChild(t, svc, hub, "member-"+member[:8])
	require.NoError(t, svc.DB.Create(&db.CircleWalletMembership{
		CircleHubAppID: hub.ID, RequesterPubkey: member, WalletAppID: child.ID,
	}).Error)
	return child
}

func frozenByAppID(t *testing.T, children []CircleChildBalance) map[uint]*bool {
	t.Helper()
	out := map[uint]*bool{}
	for _, c := range children {
		out[c.AppID] = c.Frozen
	}
	return out
}

func TestListCircleChildrenBalances_Allowlist_FrozenWhenMemberLeft(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 100_000, 3600)
	listed, removed := tests.RandomHex32(), tests.RandomHex32()
	cfg, err := svc.AppsService.GetCircleHubConfig(hub.ID)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Create(&db.CircleIdentityAllowedPubkey{CircleIdentityID: cfg.CircleIdentityID, Pubkey: listed}).Error)

	stillIn := joinedCircleChild(t, svc, hub, listed)
	left := joinedCircleChild(t, svc, hub, removed)
	noMember := createCircleChild(t, svc, hub, "no-membership-row")

	children, _, err := newTestAPIForCircleDelete(svc).ListCircleChildrenBalances(hub, 0, 0)
	require.NoError(t, err)
	frozen := frozenByAppID(t, children)

	require.NotNil(t, frozen[stillIn.ID])
	assert.False(t, *frozen[stillIn.ID], "a listed member's wallet works")
	require.NotNil(t, frozen[left.ID])
	assert.True(t, *frozen[left.ID], "a member no longer listed has a frozen wallet")
	require.NotNil(t, frozen[noMember.ID])
	assert.True(t, *frozen[noMember.ID], "no member on record is refused, so frozen")
}

func newFollowSetCircleHub(t *testing.T, svc *tests.TestService, provider string) *db.App {
	t.Helper()
	hub, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyFollowSet, ProviderPubkey: provider, FollowSetD: "family"},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	return hub
}

func TestListCircleChildrenBalances_FollowSet_FrozenFromCachedSet(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	provider, public, hidden, removed := tests.RandomHex32(), tests.RandomHex32(), tests.RandomHex32(), tests.RandomHex32()
	hub := newFollowSetCircleHub(t, ts, provider)
	publicChild := joinedCircleChild(t, ts, hub, public)
	hiddenChild := joinedCircleChild(t, ts, hub, hidden)
	leftChild := joinedCircleChild(t, ts, hub, removed)

	svc := mocks.NewMockService(t)
	svc.On("PeekFollowSet", provider, "family").
		Return(service.NewFollowSet(provider, "family", []string{public}, []string{hidden}), true).Once()
	theAPI := &api{db: ts.DB, appsSvc: ts.AppsService, svc: svc}

	children, _, err := theAPI.ListCircleChildrenBalances(hub, 0, 0)
	require.NoError(t, err)
	frozen := frozenByAppID(t, children)

	assert.False(t, *frozen[publicChild.ID])
	assert.False(t, *frozen[hiddenChild.ID], "a hidden member counts like a public one")
	assert.True(t, *frozen[leftChild.ID])
}

// The list never waits on a relay: a set not cached yet leaves Frozen unknown
// rather than guessing either way.
func TestListCircleChildrenBalances_FollowSet_NotCached_FrozenUnknown(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	provider := tests.RandomHex32()
	hub := newFollowSetCircleHub(t, ts, provider)
	child := joinedCircleChild(t, ts, hub, tests.RandomHex32())

	svc := mocks.NewMockService(t)
	svc.On("PeekFollowSet", provider, "family").Return(nil, false).Once()
	theAPI := &api{db: ts.DB, appsSvc: ts.AppsService, svc: svc}

	children, _, err := theAPI.ListCircleChildrenBalances(hub, 0, 0)
	require.NoError(t, err)
	assert.Nil(t, frozenByAppID(t, children)[child.ID])
}
