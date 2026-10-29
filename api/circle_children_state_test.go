package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/getinfocache"
	"github.com/flokiorg/lokihub/tests"
)

// WP-19 item 3: the wallet list shows each wallet's state, grace, dust, freeze
// and pending rotation, and its member even once the membership row is gone.
func TestListCircleChildrenBalances_StateGraceFrozenRotation(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := newCircleHub(t, svc, 100_000, 3600)

	active := createCircleChild(t, svc, hub, "active")
	closing := createCircleChild(t, svc, hub, "closing")
	graceEnds := time.Now().Add(time.Hour).Unix()
	dust := int64(1000)
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", closing.ID).Updates(map[string]interface{}{
		"circle_state": circlelife.StateClosing, "circle_grace_ends_at": graceEnds, "circle_frozen": true,
		"circle_dust_threshold_mloki": dust}).Error)
	member := tests.RandomHex32()
	require.NoError(t, svc.DB.Create(&db.CircleJoinRecord{CircleHubAppID: hub.ID, HubWalletPubkey: *hub.WalletPubkey, HubConnPubkey: hub.AppPubkey,
		AuthorizerPubkey: member, ConnectionPubkey: closing.AppPubkey, ProofCreatedAt: 1, Deadline: 301, EncryptedDetails: "x", WalletAppID: closing.ID}).Error)
	newKey := tests.RandomHex32()
	require.NoError(t, svc.DB.Create(&db.CircleRotation{WalletAppID: closing.ID, MemberPubkey: member, NewConnectionPubkey: newKey,
		RequestedAt: 1, CompletesAt: graceEnds, State: db.CircleRotationPending}).Error)

	children, _, err := newTestAPIForCircleDelete(svc).ListCircleChildrenBalances(hub, 0, 0)
	require.NoError(t, err)
	byID := map[uint]CircleChildBalance{}
	for _, c := range children {
		byID[c.AppID] = c
	}
	assert.Equal(t, circlelife.StateActive, byID[active.ID].State)
	assert.False(t, byID[active.ID].Frozen)
	c := byID[closing.ID]
	assert.Equal(t, circlelife.StateClosing, c.State)
	require.NotNil(t, c.GraceEndsAt)
	assert.Equal(t, graceEnds, *c.GraceEndsAt)
	assert.True(t, c.Frozen)
	assert.Equal(t, dust, c.DustThresholdMloki)
	assert.Equal(t, member, c.RequesterPubkey, "read from the join record")
	require.NotNil(t, c.PendingRotation)
	assert.Equal(t, newKey, c.PendingRotation.ConnectionPubkey)
}

// WP-19 item 12: the host closes a circle to new members and opens it again; a
// torn-down circle stays closed.
func TestSetCircleAccepting(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := newCircleHub(t, svc, 100_000, 3600)
	theAPI := newTestAPIForCircleDelete(svc)

	require.NoError(t, theAPI.SetCircleAccepting(hub, false))
	cfg, err := svc.AppsService.GetCircleHubConfig(hub.ID)
	require.NoError(t, err)
	require.NotNil(t, cfg.AcceptingJoins)
	assert.False(t, *cfg.AcceptingJoins)
	require.NoError(t, theAPI.SetCircleAccepting(hub, true))
	cfg, err = svc.AppsService.GetCircleHubConfig(hub.ID)
	require.NoError(t, err)
	assert.True(t, *cfg.AcceptingJoins)

	require.NoError(t, svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", hub.ID).Update("teardown_requested_at", 1).Error)
	assert.Error(t, theAPI.SetCircleAccepting(hub, true))
}

// R2-L3: a change of accepting, either way, drops the cached get_info of the
// circle's listed connections at once; another circle's stays.
func TestSetCircleAccepting_DropsTheListedConnectionsCachedGetInfo(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := newCircleHub(t, svc, 100_000, 3600)
	other := newCircleHub(t, svc, 100_000, 3600)
	conn := &db.App{Name: "listed", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleHubConn, ParentAppID: &hub.ID}
	otherConn := &db.App{Name: "listed2", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleHubConn, ParentAppID: &other.ID}
	require.NoError(t, svc.DB.Create(conn).Error)
	require.NoError(t, svc.DB.Create(otherConn).Error)
	theAPI := newTestAPIForCircleDelete(svc)

	for _, accepting := range []bool{false, true} {
		stamp := getinfocache.Listed.Begin()
		require.True(t, getinfocache.Listed.Put(conn.ID, stamp, "cached"))
		require.True(t, getinfocache.Listed.Put(otherConn.ID, stamp, "cached"))
		require.NoError(t, theAPI.SetCircleAccepting(hub, accepting))
		_, ok := getinfocache.Listed.Get(conn.ID)
		assert.False(t, ok, "accepting %v", accepting)
		_, ok = getinfocache.Listed.Get(otherConn.ID)
		assert.True(t, ok, "another circle's connection keeps its result")
	}
}
