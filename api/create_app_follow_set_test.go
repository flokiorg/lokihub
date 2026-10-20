package api

import (
	"context"
	"errors"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/service"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/tests/mocks"
)

var nodeIdentity = tests.RandomHex32()

func newFollowSetTestAPI(t *testing.T) (*api, *mocks.MockService, *tests.TestService) {
	t.Helper()
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(ts.Remove)
	svc := mocks.NewMockService(t)
	svc.On("NodeNostrIdentity").Return(nodeIdentity, nil).Maybe()
	return &api{db: ts.DB, appsSvc: ts.AppsService, cfg: ts.Cfg, keys: ts.Keys, svc: svc}, svc, ts
}

func followSetCircleRequest(provider string, set *FollowSetRequest) *CreateAppRequest {
	return &CreateAppRequest{
		Name:                    "My Circle",
		Kind:                    db.AppKindCircleHub,
		Scopes:                  []string{constants.CIRCLE_WALLET_SCOPE},
		CirclePerWalletMaxMloki: 100_000,
		CircleMaxExpSecs:        3600,
		CircleIdentityName:      "family",
		CirclePolicy:            db.CirclePolicyFollowSet,
		ProviderPubkey:          provider,
		FollowSetD:              "family",
		FollowSet:               set,
	}
}

func countRows(t *testing.T, ts *tests.TestService, model interface{}) int64 {
	t.Helper()
	var n int64
	require.NoError(t, ts.DB.Model(model).Count(&n).Error)
	return n
}

// A new set this hub publishes is published before the hub exists, as the
// node identity, and the invite carries the set's title (NIP-CW §Membership).
func TestCreateApp_FollowSet_PublishesThenCreates(t *testing.T) {
	theAPI, svc, ts := newFollowSetTestAPI(t)
	member := tests.RandomHex32()
	hidden := tests.RandomHex32()
	published := &service.FollowSet{Provider: nodeIdentity, D: "family", Title: "Ada's Family Circle", Public: []string{member}, Hidden: []string{hidden}}

	svc.On("PublishFollowSet", mock.Anything, "family", service.FollowSetContent{
		Title: "Ada's Family Circle", Public: []string{member}, Hidden: []string{hidden},
	}).Run(func(mock.Arguments) {
		assert.Zero(t, countRows(t, ts, &db.App{}), "the set is published before the hub is created")
	}).Return(published, nil).Once()
	svc.On("PeekFollowSet", nodeIdentity, "family").Return(published, true).Once()

	resp, err := theAPI.CreateApp(followSetCircleRequest("", &FollowSetRequest{
		Title: "Ada's Family Circle", PublicMembers: []string{member}, HiddenMembers: []string{hidden},
	}))
	require.NoError(t, err)

	cfg, err := ts.AppsService.GetCircleHubConfig(resp.Id)
	require.NoError(t, err)
	assert.Equal(t, db.CirclePolicyFollowSet, cfg.CircleIdentity.Policy)
	assert.Equal(t, nodeIdentity, cfg.CircleIdentity.ProviderPubkey, "an empty provider means the node identity")
	assert.Equal(t, "family", cfg.CircleIdentity.FollowSetD)

	require.NotNil(t, resp.CircleHubToken)
	token, err := nipcw.DecodeCircleHubConnection(*resp.CircleHubToken)
	require.NoError(t, err)
	assert.Equal(t, "Ada's Family Circle", token.Label, "the invite defaults to the set's title")
}

// A set with no title leaves the label at the hub's name.
func TestCreateApp_FollowSet_UntitledSetKeepsHubName(t *testing.T) {
	theAPI, svc, _ := newFollowSetTestAPI(t)
	set := &service.FollowSet{Provider: nodeIdentity, D: "family"}
	svc.On("PublishFollowSet", mock.Anything, "family", mock.Anything).Return(set, nil).Once()
	svc.On("PeekFollowSet", nodeIdentity, "family").Return(set, true).Once()

	resp, err := theAPI.CreateApp(followSetCircleRequest("", &FollowSetRequest{}))
	require.NoError(t, err)
	token, err := nipcw.DecodeCircleHubConnection(*resp.CircleHubToken)
	require.NoError(t, err)
	assert.Equal(t, "My Circle", token.Label)
}

// A set that can't be published creates nothing: no identity, no hub.
func TestCreateApp_FollowSet_PublishFailureCreatesNothing(t *testing.T) {
	theAPI, svc, ts := newFollowSetTestAPI(t)
	svc.On("PublishFollowSet", mock.Anything, "family", mock.Anything).
		Return(nil, errors.New("no relay accepted the node event")).Once()

	_, err := theAPI.CreateApp(followSetCircleRequest("", &FollowSetRequest{PublicMembers: []string{tests.RandomHex32()}}))
	require.ErrorContains(t, err, "no relay accepted")
	assert.Zero(t, countRows(t, ts, &db.App{}))
	assert.Zero(t, countRows(t, ts, &db.CircleIdentity{}))
}

// lokihub can sign only as the node, so it never publishes a set under another
// account, and never touches a relay trying.
func TestCreateApp_FollowSet_OtherProviderCannotBePublished(t *testing.T) {
	theAPI, _, ts := newFollowSetTestAPI(t)

	_, err := theAPI.CreateApp(followSetCircleRequest(tests.RandomHex32(), &FollowSetRequest{Title: "x"}))
	require.ErrorIs(t, err, constants.ErrInvalidParams)
	assert.Zero(t, countRows(t, ts, &db.CircleIdentity{}))
}

// A set someone else publishes is only read: the hub is created, the set is
// fetched, and its title still labels the invite.
func TestCreateApp_FollowSet_ReadOnlySet(t *testing.T) {
	theAPI, svc, ts := newFollowSetTestAPI(t)
	provider := tests.RandomHex32()
	set := &service.FollowSet{Provider: provider, D: "family", Title: "Their Circle"}
	svc.On("PeekFollowSet", provider, "family").Return(nil, false).Once()
	svc.On("RefreshFollowSet", mock.Anything, provider, "family").Return(set, nil).Once()

	resp, err := theAPI.CreateApp(followSetCircleRequest(provider, nil))
	require.NoError(t, err)

	cfg, err := ts.AppsService.GetCircleHubConfig(resp.Id)
	require.NoError(t, err)
	assert.Equal(t, provider, cfg.CircleIdentity.ProviderPubkey)
	assert.False(t, theAPI.isManagedFollowSet(&cfg.CircleIdentity))

	token, err := nipcw.DecodeCircleHubConnection(*resp.CircleHubToken)
	require.NoError(t, err)
	assert.Equal(t, "Their Circle", token.Label)
}

// Publishing first would overwrite the set another identity already uses.
func TestCreateApp_FollowSet_ExistingAddressRejectedBeforePublishing(t *testing.T) {
	theAPI, _, ts := newFollowSetTestAPI(t)
	_, err := ts.AppsService.CreateCircleIdentity("first", db.CirclePolicyFollowSet, nodeIdentity, "family")
	require.NoError(t, err)

	_, err = theAPI.CreateApp(followSetCircleRequest("", &FollowSetRequest{Title: "again"}))
	require.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestPublishCircleIdentityFollowSet_OnlyForSetsThisHubAuthors(t *testing.T) {
	theAPI, svc, ts := newFollowSetTestAPI(t)
	theirs, err := ts.AppsService.CreateCircleIdentity("theirs", db.CirclePolicyFollowSet, tests.RandomHex32(), "family")
	require.NoError(t, err)
	ours, err := ts.AppsService.CreateCircleIdentity("ours", db.CirclePolicyFollowSet, nodeIdentity, "family")
	require.NoError(t, err)
	allowlist, err := ts.AppsService.CreateCircleIdentity("list", db.CirclePolicyAllowlist, "", "")
	require.NoError(t, err)

	_, err = theAPI.PublishCircleIdentityFollowSet(context.Background(), theirs.ID, &FollowSetRequest{})
	require.ErrorIs(t, err, constants.ErrInvalidParams)
	_, err = theAPI.PublishCircleIdentityFollowSet(context.Background(), allowlist.ID, &FollowSetRequest{})
	require.ErrorIs(t, err, constants.ErrInvalidParams)

	member := tests.RandomHex32()
	svc.On("PublishFollowSet", mock.Anything, "family", service.FollowSetContent{Title: "Family", Public: []string{member}}).
		Return(&service.FollowSet{Provider: nodeIdentity, D: "family", Title: "Family", Public: []string{member},
			Event: &nostr.Event{}}, nil).Once()
	svc.On("GetPrivateTransportStatus").Return(service.PrivateTransportStatus{NodeIdentity: nodeIdentity}).Once()

	resp, err := theAPI.PublishCircleIdentityFollowSet(context.Background(), ours.ID, &FollowSetRequest{Title: "Family", PublicMembers: []string{member}})
	require.NoError(t, err)
	assert.True(t, resp.Managed)
	assert.True(t, resp.Resolved)
	assert.True(t, resp.HiddenMembersAvailable)
	assert.Equal(t, "30000:"+nodeIdentity+":family", resp.Address)
	assert.Equal(t, []string{member}, resp.PublicMembers)
	assert.Equal(t, []string{}, resp.HiddenMembers)
}

// An unfound set reads as unresolved, not as an error or an empty set.
func TestGetCircleIdentityFollowSet_Unresolved(t *testing.T) {
	theAPI, svc, ts := newFollowSetTestAPI(t)
	provider := tests.RandomHex32()
	identity, err := ts.AppsService.CreateCircleIdentity("theirs", db.CirclePolicyFollowSet, provider, "family")
	require.NoError(t, err)
	svc.On("FollowSet", mock.Anything, provider, "family").Return(nil, constants.ErrCircleMembershipUnresolved).Once()
	svc.On("GetPrivateTransportStatus").Return(service.PrivateTransportStatus{}).Once()

	resp, err := theAPI.GetCircleIdentityFollowSet(context.Background(), identity.ID)
	require.NoError(t, err)
	assert.False(t, resp.Resolved)
	assert.False(t, resp.Managed)
	assert.False(t, resp.HiddenMembersAvailable)
}

// The inline summary reports public and hidden members together, the set's
// title, and whether this hub publishes it.
func TestBuildCircleIdentityCounts_FollowSet(t *testing.T) {
	theAPI, svc, ts := newFollowSetTestAPI(t)
	identity, err := ts.AppsService.CreateCircleIdentity("ours", db.CirclePolicyFollowSet, nodeIdentity, "family")
	require.NoError(t, err)

	svc.On("PeekFollowSet", nodeIdentity, "family").Return(nil, false).Once()
	summary, err := theAPI.buildCircleIdentityCounts(context.Background(), identity, false)
	require.NoError(t, err)
	assert.Nil(t, summary.MemberCount, "not read yet is unknown, not zero")
	assert.True(t, summary.Managed)
	assert.Equal(t, "family", summary.FollowSetD)

	set := &service.FollowSet{Title: "Family", Public: []string{tests.RandomHex32()}, Hidden: []string{tests.RandomHex32()}}
	svc.On("PeekFollowSet", nodeIdentity, "family").Return(set, true).Once()
	summary, err = theAPI.buildCircleIdentityCounts(context.Background(), identity, false)
	require.NoError(t, err)
	require.NotNil(t, summary.MemberCount)
	assert.Equal(t, 2, *summary.MemberCount)
	assert.Equal(t, "Family", summary.FollowSetTitle)
}
