package service

import (
	"context"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// U-17.7: a version dated more than 15 minutes ahead is ignored, never adopted.
func TestQueryAndStore_FutureVersionIgnored(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()
	sk, provider := newKeypair(t)
	now := signedFollowSetEvent(t, sk, testD, []string{requesterPubkey})
	future := signedFollowSetEvent(t, sk, testD, []string{unrelatedPubkey})
	future.CreatedAt = nostr.Timestamp(time.Now().Add(16 * time.Minute).Unix())
	require.NoError(t, future.Sign(sk))
	useRelayServing(t, svc, now, future)

	set, err := cache.queryAndStore(context.Background(), nostr.NewSimplePool(context.Background()), provider, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey))
	assert.False(t, set.IsMember(unrelatedPubkey), "the future-dated version is not adopted")
}

// U-17.6 (Q21): once a version is resolved, a fetch finding only an older one is
// a failed refresh: the last resolution stays in force and no resolution is
// applied.
func TestQueryAndStore_OlderVersionIsAFailedRefresh(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()
	sk, provider := newKeypair(t)
	older := signedFollowSetEvent(t, sk, testD, []string{unrelatedPubkey})
	older.CreatedAt -= 60
	require.NoError(t, older.Sign(sk))
	newer := signedFollowSetEvent(t, sk, testD, []string{requesterPubkey})

	applied := 0
	cache.onResolved = func(_, _ string, _ *FollowSet) { applied++ }
	useRelayServing(t, svc, newer)
	_, err := cache.queryAndStore(context.Background(), nostr.NewSimplePool(context.Background()), provider, testD)
	require.NoError(t, err)
	require.Equal(t, 1, applied)

	useRelayServing(t, svc, older)
	set, err := cache.queryAndStore(context.Background(), nostr.NewSimplePool(context.Background()), provider, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey), "the last resolution stays in force")
	assert.False(t, set.IsMember(unrelatedPubkey))
	assert.Equal(t, 1, applied, "a failed refresh applies nothing")
}

// U-17.3: hidden members encrypted to a retired inbox key still in use are read.
func TestParseFollowSet_RetiredInboxKeyStillReads(t *testing.T) {
	providerSK, _ := newKeypair(t)
	currentSK, currentPub := newKeypair(t)
	retiredSK, retiredPub := newKeypair(t)
	_, oldMember := newKeypair(t)
	_, newMember := newKeypair(t)
	ev := signedFollowSetEvent(t, providerSK, testD, nil,
		hiddenEntry(t, providerSK, retiredPub, oldMember), hiddenEntry(t, providerSK, currentPub, newMember))

	set := parseFollowSet(&ev, testD, currentSK)
	assert.False(t, set.IsMember(oldMember))
	set = parseFollowSet(&ev, testD, currentSK, retiredSK)
	assert.True(t, set.IsMember(oldMember))
	assert.True(t, set.IsMember(newMember))
}

// U-17.1, U-17.2, U-17.4: a successful resolution suspends the Active wallets
// of members it no longer names, and alerts when that is a large share; an
// unresolved refresh suspends nothing.
func TestApplyFollowSetResolution(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	_, provider := newKeypair(t)
	identity := db.CircleIdentity{Name: "c", Policy: db.CirclePolicyFollowSet, ProviderPubkey: provider, FollowSetD: testD}
	require.NoError(t, svc.DB.Create(&identity).Error)
	hub := &db.App{Name: "hub", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleHub}
	require.NoError(t, svc.DB.Create(hub).Error)
	_, err = tests.AddCircleHubConfig(svc, hub.ID)
	require.NoError(t, err)
	// AddCircleHubConfig makes its own identity: point the config at this one.
	require.NoError(t, svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", hub.ID).Update("circle_identity_id", identity.ID).Error)

	members := map[string]*db.App{}
	for range 4 {
		_, member := newKeypair(t)
		w := &db.App{Name: "w", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleWallet, ParentAppID: &hub.ID,
			ParentKind: db.ParentKindCircle, CircleState: circlelife.StateActive}
		require.NoError(t, svc.DB.Create(w).Error)
		require.NoError(t, svc.DB.Create(&db.CircleWalletMembership{CircleHubAppID: hub.ID, RequesterPubkey: member, WalletAppID: w.ID}).Error)
		members[member] = w
	}
	var dropped string
	keep := map[string]bool{}
	for pk := range members {
		if dropped == "" {
			dropped = pk
			continue
		}
		keep[pk] = true
	}

	alerts := tests.NewMockEventConsumer()
	svc.EventPublisher.RegisterSubscriber(alerts)
	ApplyFollowSetResolution(svc.DB, svc.EventPublisher, provider, testD, func(pk string) bool { return keep[pk] }, time.Now())

	state := func(w *db.App) string {
		var stored db.App
		require.NoError(t, svc.DB.First(&stored, w.ID).Error)
		return circlelife.State(&stored)
	}
	assert.Equal(t, circlelife.StateSuspended, state(members[dropped]))
	for pk := range keep {
		assert.Equal(t, circlelife.StateActive, state(members[pk]))
	}
	require.Eventually(t, func() bool { return len(alerts.GetConsumedEvents()) > 0 }, time.Second, 10*time.Millisecond,
		"1 of 4 Active wallets is 25 %, over the 20 % alert")
	ev := alerts.GetConsumedEvents()[0]
	assert.Equal(t, "nwc_circle_mass_suspension", ev.Event)
	props := ev.Properties.(map[string]interface{})
	assert.Equal(t, 1, props["suspended"])
	assert.NotContains(t, props, "pubkey", "no member pubkey in the alert")
}
