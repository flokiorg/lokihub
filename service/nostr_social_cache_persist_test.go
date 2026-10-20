package service

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// switchableRelay serves whichever follow-set versions it was last given.
type switchableRelay struct {
	mu   sync.Mutex
	evts []nostr.Event
}

func (r *switchableRelay) serve(evts ...nostr.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evts = evts
}

func useSwitchableRelay(t *testing.T, svc *tests.TestService) *switchableRelay {
	t.Helper()
	r := &switchableRelay{}
	relay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		if slices.Contains(filter.Kinds, db.KindFollowSet) {
			r.mu.Lock()
			evts := slices.Clone(r.evts)
			r.mu.Unlock()
			sendEvents(t, conn, subID, evts...)
			return
		}
		sendEOSE(t, conn, subID)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))
	return r
}

// startCache runs a fresh cache the way the hub does at startup and waits for
// its first refresh pass. Each call stands for one process lifetime.
func startCache(t *testing.T, svc *tests.TestService) *nostrSocialCache {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cache := NewNostrSocialCache(svc.Cfg)
	StartNostrSocialCacheRefresher(ctx, svc.DB, cache, nostr.NewSimplePool(ctx))
	require.Eventually(t, cache.ready.Load, 10*time.Second, 10*time.Millisecond)
	return cache
}

func TestFollowSetIdentity_HasLastEventColumn(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	assert.True(t, svc.DB.Migrator().HasColumn(&db.CircleIdentity{}, "last_follow_set_event"))
}

// The provider removed a member in v2. After a restart, relays that only still
// serve v1 must not readmit them: the newest version this hub has seen is the
// floor, across process lifetimes.
func TestFollowSet_RestartDoesNotRollBackToAnOlderVersion(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	providerSK, provider := newKeypair(t)
	identity := db.CircleIdentity{Name: "family", Policy: db.CirclePolicyFollowSet, ProviderPubkey: provider, FollowSetD: testD}
	require.NoError(t, svc.DB.Create(&identity).Error)

	v2 := signedFollowSetEvent(t, providerSK, testD, []string{unrelatedPubkey})
	v1 := signedFollowSetEvent(t, providerSK, testD, []string{requesterPubkey, unrelatedPubkey})
	v1.CreatedAt = v2.CreatedAt - 60
	require.NoError(t, v1.Sign(providerSK))

	relay := useSwitchableRelay(t, svc)
	relay.serve(v2)
	first := startCache(t, svc)
	ok, err := first.IsAuthorized(context.Background(), requesterPubkey, &identity, svc.DB)
	require.NoError(t, err)
	require.False(t, ok, "v2 removed this member")

	relay.serve(v1)
	second := startCache(t, svc)
	ok, err = second.IsAuthorized(context.Background(), requesterPubkey, &identity, svc.DB)
	require.NoError(t, err)
	assert.False(t, ok, "an older version from the relays must not readmit a removed member after a restart")
	ok, err = second.IsAuthorized(context.Background(), unrelatedPubkey, &identity, svc.DB)
	require.NoError(t, err)
	assert.True(t, ok, "members the newest version lists stay in")
}

// A newer version still replaces the persisted one, so the floor never
// blocks a provider's real changes.
func TestFollowSet_RestartStillTakesANewerVersion(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	providerSK, provider := newKeypair(t)
	identity := db.CircleIdentity{Name: "family", Policy: db.CirclePolicyFollowSet, ProviderPubkey: provider, FollowSetD: testD}
	require.NoError(t, svc.DB.Create(&identity).Error)

	v1 := signedFollowSetEvent(t, providerSK, testD, []string{unrelatedPubkey})
	v1.CreatedAt -= 60
	require.NoError(t, v1.Sign(providerSK))
	v2 := signedFollowSetEvent(t, providerSK, testD, []string{requesterPubkey})

	relay := useSwitchableRelay(t, svc)
	relay.serve(v1)
	startCache(t, svc)

	relay.serve(v2)
	second := startCache(t, svc)
	ok, err := second.IsAuthorized(context.Background(), requesterPubkey, &identity, svc.DB)
	require.NoError(t, err)
	assert.True(t, ok)
}
