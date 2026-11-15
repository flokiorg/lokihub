package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// Well-formed 64-char lowercase-hex placeholders — apps.CreateCircleIdentity
// validates ProviderPubkey's format, so a short opaque placeholder no longer
// round-trips through CreateCircleHub.
const (
	providerPubkey  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	requesterPubkey = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	unrelatedPubkey = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// testD is the `d` identifier most tests' follow set uses.
const testD = "family"

// seedCache injects a resolved set directly into the cache, bypassing relays.
func seedCache(s *nostrSocialCache, provider, d string, members []string, age time.Duration) {
	set := &FollowSet{
		Provider:  provider,
		D:         d,
		Public:    members,
		Event:     &nostr.Event{PubKey: provider, Kind: db.KindFollowSet, CreatedAt: nostr.Now() - nostr.Timestamp(age.Seconds())},
		FetchedAt: time.Now().Add(-age),
		members:   make(map[string]struct{}, len(members)),
	}
	for _, p := range members {
		set.members[p] = struct{}{}
	}
	s.mu.Lock()
	s.cache[followSetKey(provider, d)] = set
	s.mu.Unlock()
}

func cached(s *nostrSocialCache, provider, d string) (*FollowSet, bool) {
	return s.peek(provider, d)
}

func newSocialCacheForTest(t *testing.T) (*nostrSocialCache, *tests.TestService) {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	cache := NewNostrSocialCache(svc.Cfg)
	return cache, svc
}

// makeIdentity builds a CircleIdentity for follow-set tests where no DB lookup
// is needed (ID is irrelevant).
func makeIdentity(policy, provPubkey string) *db.CircleIdentity {
	return &db.CircleIdentity{
		Policy:         policy,
		ProviderPubkey: provPubkey,
		FollowSetD:     testD,
	}
}

// ---------------------------------------------------------------------------
// Allowlist policy
// ---------------------------------------------------------------------------

func TestIsAuthorized_Allowlist_Allowed(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	// Create a real circle_hub app so CircleIdentityAllowedPubkey has a valid foreign key.
	app, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	cfg, err := svc.AppsService.GetCircleHubConfig(app.ID)
	require.NoError(t, err)
	svc.DB.Create(&db.CircleIdentityAllowedPubkey{CircleIdentityID: cfg.CircleIdentityID, Pubkey: requesterPubkey})

	ok, err := cache.IsAuthorized(context.TODO(), requesterPubkey, &cfg.CircleIdentity, svc.DB)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestIsAuthorized_Allowlist_Denied(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	app, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	cfg, err := svc.AppsService.GetCircleHubConfig(app.ID)
	require.NoError(t, err)
	// requesterPubkey NOT in allowlist
	svc.DB.Create(&db.CircleIdentityAllowedPubkey{CircleIdentityID: cfg.CircleIdentityID, Pubkey: unrelatedPubkey})

	ok, err := cache.IsAuthorized(context.TODO(), requesterPubkey, &cfg.CircleIdentity, svc.DB)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestIsAuthorized_Allowlist_EmptyList(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	app, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	cfg, err := svc.AppsService.GetCircleHubConfig(app.ID)
	require.NoError(t, err)
	// no rows inserted

	ok, err := cache.IsAuthorized(context.TODO(), requesterPubkey, &cfg.CircleIdentity, svc.DB)
	require.NoError(t, err)
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// follow_set policy: the provider's set must list requesterPubkey
// ---------------------------------------------------------------------------

func TestIsAuthorized_FollowSet_Allowed(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	seedCache(cache, providerPubkey, testD, []string{requesterPubkey}, 0)

	identity := makeIdentity(db.CirclePolicyFollowSet, providerPubkey)
	ok, err := cache.IsAuthorized(context.TODO(), requesterPubkey, identity, svc.DB)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestIsAuthorized_FollowSet_Denied(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	seedCache(cache, providerPubkey, testD, []string{unrelatedPubkey}, 0)

	identity := makeIdentity(db.CirclePolicyFollowSet, providerPubkey)
	ok, err := cache.IsAuthorized(context.TODO(), requesterPubkey, identity, svc.DB)
	require.NoError(t, err)
	assert.False(t, ok)
}

// One provider can run several circles, one per `d`. Membership in one set
// must not admit anyone to another.
func TestIsAuthorized_FollowSet_ScopedToItsD(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	seedCache(cache, providerPubkey, "family", []string{requesterPubkey}, 0)
	seedCache(cache, providerPubkey, "work", []string{unrelatedPubkey}, 0)

	family := &db.CircleIdentity{Policy: db.CirclePolicyFollowSet, ProviderPubkey: providerPubkey, FollowSetD: "family"}
	work := &db.CircleIdentity{Policy: db.CirclePolicyFollowSet, ProviderPubkey: providerPubkey, FollowSetD: "work"}

	ok, err := cache.IsAuthorized(context.TODO(), requesterPubkey, family, svc.DB)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = cache.IsAuthorized(context.TODO(), requesterPubkey, work, svc.DB)
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestIsAuthorized_FollowSet_NotReady_ReturnsWarmingUp covers the hub-still-
// starting-up case: a fresh cache (ready defaults to false until
// StartNostrSocialCacheRefresher's initial pass completes) with no entry yet
// for this set must refuse fast with ErrSocialCacheWarmingUp instead of
// attempting a relay fetch.
func TestIsAuthorized_FollowSet_NotReady_ReturnsWarmingUp(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	_, ok := cached(cache, providerPubkey, testD)
	require.False(t, ok, "precondition: nothing cached yet for this set")

	identity := makeIdentity(db.CirclePolicyFollowSet, providerPubkey)
	authorized, err := cache.IsAuthorized(context.TODO(), requesterPubkey, identity, svc.DB)
	assert.ErrorIs(t, err, constants.ErrSocialCacheWarmingUp)
	assert.False(t, authorized)

	_, ok = cached(cache, providerPubkey, testD)
	assert.False(t, ok, "the readiness gate must not itself populate the cache")
}

// The gate only blocks a true "never fetched" case: once this set has any
// cached entry, the overall ready flag doesn't matter.
func TestIsAuthorized_FollowSet_NotReady_ButCached_EvaluatesNormally(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	seedCache(cache, providerPubkey, testD, []string{requesterPubkey}, 0)

	identity := makeIdentity(db.CirclePolicyFollowSet, providerPubkey)
	authorized, err := cache.IsAuthorized(context.TODO(), requesterPubkey, identity, svc.DB)
	require.NoError(t, err)
	assert.True(t, authorized)
}

// The readiness gate is scoped to follow_set only — allowlist authorization
// (a plain DB query) must never be gated on the cache's warm-up state.
func TestIsAuthorized_Allowlist_NotReady_Unaffected(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	app, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	cfg, err := svc.AppsService.GetCircleHubConfig(app.ID)
	require.NoError(t, err)
	svc.DB.Create(&db.CircleIdentityAllowedPubkey{CircleIdentityID: cfg.CircleIdentityID, Pubkey: requesterPubkey})

	ok, err := cache.IsAuthorized(context.TODO(), requesterPubkey, &cfg.CircleIdentity, svc.DB)
	require.NoError(t, err)
	assert.True(t, ok)
}

// ---------------------------------------------------------------------------
// Unsupported policy: "followers"/"both" were removed because they check the
// requester's own self-published lists, which provide no real access control
// (anyone can fabricate one for free), and "following" (kind:3) was replaced
// by follow_set. IsAuthorized must deny any policy value it doesn't recognize.
// ---------------------------------------------------------------------------

func TestIsAuthorized_UnsupportedPolicy_Denied(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	for _, policy := range []string{"followers", "following"} {
		seedCache(cache, providerPubkey, testD, []string{requesterPubkey}, 0)
		identity := makeIdentity(policy, providerPubkey)
		ok, err := cache.IsAuthorized(context.TODO(), requesterPubkey, identity, svc.DB)
		require.NoError(t, err)
		assert.False(t, ok, "policy %q must not admit anyone", policy)
	}
}

// ---------------------------------------------------------------------------
// Cache behaviour
// ---------------------------------------------------------------------------

func TestGetFollowSet_CacheHit_NoRefetch(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	seedCache(cache, providerPubkey, testD, []string{requesterPubkey}, 0)
	before, _ := cached(cache, providerPubkey, testD)

	set, err := cache.getFollowSet(context.TODO(), providerPubkey, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey))

	after, _ := cached(cache, providerPubkey, testD)
	assert.Equal(t, before.FetchedAt, after.FetchedAt, "cache hit must not update FetchedAt")
}

func TestGetFollowSet_StaleEntry_ServedWithoutRefetch(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	seedCache(cache, providerPubkey, testD, []string{requesterPubkey}, socialCacheTTL+time.Second)
	before, _ := cached(cache, providerPubkey, testD)

	set, err := cache.getFollowSet(context.TODO(), providerPubkey, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey))

	after, _ := cached(cache, providerPubkey, testD)
	assert.Equal(t, before.FetchedAt, after.FetchedAt, "a stale-but-present entry must be served as-is, never refetched on the request path")
}

// Past the eviction cutoff an entry is too old to decide membership on: the
// request path refetches it, so a member the provider since removed is not
// admitted from the old copy when the refresher isn't running.
func TestGetFollowSet_EntryPastEvictionCutoff_IsRefetched(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	providerSK, provider := newKeypair(t)
	current := signedFollowSetEvent(t, providerSK, testD, []string{unrelatedPubkey})
	useRelayServing(t, svc, current)
	seedCache(cache, provider, testD, []string{requesterPubkey}, 2*socialCacheTTL+time.Minute)

	set, err := cache.getFollowSet(context.TODO(), provider, testD)
	require.NoError(t, err)
	assert.False(t, set.IsMember(requesterPubkey), "the removed member is not admitted from the expired copy")
	assert.True(t, set.IsMember(unrelatedPubkey))
}

// With no relay able to serve the set, an expired entry is unresolved rather
// than served: NOT_READY, never the old member list.
func TestGetFollowSet_EntryPastEvictionCutoff_NoRelay_IsUnresolved(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	_, provider := newKeypair(t)
	useRelayServing(t, svc)
	seedCache(cache, provider, testD, []string{requesterPubkey}, 2*socialCacheTTL+time.Minute)

	_, err := cache.getFollowSet(context.TODO(), provider, testD)
	require.ErrorIs(t, err, constants.ErrCircleMembershipUnresolved)
}

func TestGetFollowSet_ColdMiss_TriggersFetch(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()
	provider, evt := newSignedFollowSet(t, testD, []string{requesterPubkey})
	useRelayServing(t, svc, evt)

	_, ok := cached(cache, provider, testD)
	require.False(t, ok, "precondition: nothing cached yet for this set")

	set, err := cache.getFollowSet(context.TODO(), provider, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey))

	entry, ok := cached(cache, provider, testD)
	require.True(t, ok, "a true cold miss must populate the cache")
	assert.WithinDuration(t, time.Now(), entry.FetchedAt, 5*time.Second)
}

func TestRefreshFollowSet_PopulatesCache(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()
	provider, evt := newSignedFollowSet(t, testD, []string{requesterPubkey})
	useRelayServing(t, svc, evt)

	set, err := cache.RefreshFollowSet(context.TODO(), provider, testD)
	require.NoError(t, err)

	entry, ok := cached(cache, provider, testD)
	require.True(t, ok, "RefreshFollowSet must populate the cache")
	assert.Same(t, entry, set, "the returned set must be the one stored in the cache")
}

// TestRefreshOne_UpdatesStaleEntry_OnSuccessfulFetch exercises a refresh that
// actually confirms something against a real relay. A refresh that finds
// nothing must NOT be treated as having updated the entry — see
// TestQueryAndStore_FailedRefresh_PreservesPreviousGoodCache.
func TestRefreshOne_UpdatesStaleEntry_OnSuccessfulFetch(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	provider, evt := newSignedFollowSet(t, testD, []string{requesterPubkey})
	useRelayServing(t, svc, evt)

	seedCache(cache, provider, testD, []string{unrelatedPubkey}, socialCacheTTL+time.Second)
	before, _ := cached(cache, provider, testD)

	ctx := context.Background()
	require.NoError(t, cache.refreshOne(ctx, nostr.NewSimplePool(ctx), provider, testD))

	after, _ := cached(cache, provider, testD)
	assert.True(t, after.FetchedAt.After(before.FetchedAt), "refreshOne must replace a stale entry once the relay actually confirms one")
	assert.True(t, after.IsMember(requesterPubkey))
	assert.False(t, after.IsMember(unrelatedPubkey))
}

// ---------------------------------------------------------------------------
// PeekFollowSet / FollowSet
// ---------------------------------------------------------------------------

func TestPeekFollowSet_CacheMiss_NeverFetches(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	_, ok := cache.PeekFollowSet(providerPubkey, testD)
	assert.False(t, ok, "a true cache miss must report ok=false, never trigger a fetch")

	_, stillMissing := cached(cache, providerPubkey, testD)
	assert.False(t, stillMissing, "PeekFollowSet must never populate the cache")
}

func TestPeekFollowSet_CacheHit_ReturnsCount(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	seedCache(cache, providerPubkey, testD, []string{requesterPubkey, unrelatedPubkey}, 0)

	set, ok := cache.PeekFollowSet(providerPubkey, testD)
	require.True(t, ok)
	assert.Equal(t, 2, set.MemberCount())
}

func TestFollowSet_ColdFetch(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()
	provider, evt := newSignedFollowSet(t, testD, []string{requesterPubkey, unrelatedPubkey})
	useRelayServing(t, svc, evt)

	set, err := cache.FollowSet(context.TODO(), provider, testD)
	require.NoError(t, err)
	assert.Equal(t, 2, set.MemberCount())

	_, ok := cached(cache, provider, testD)
	assert.True(t, ok, "FollowSet may cold-fetch, populating the cache")
}

// ---------------------------------------------------------------------------
// Periodic background refresh
// ---------------------------------------------------------------------------

func TestRunSocialCacheRefresh_SkipsBadIdentity_ContinuesSweep(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	provider, evt := newSignedFollowSet(t, testD, []string{requesterPubkey})
	useRelayServing(t, svc, evt)

	// A follow_set identity with no d must be skipped rather than aborting the
	// sweep. It can't be created through the service, so it is seeded directly.
	legacy := db.CircleIdentity{Name: "no-d-circle", Policy: db.CirclePolicyFollowSet, ProviderPubkey: provider}
	require.NoError(t, svc.DB.Create(&legacy).Error)
	_, _, err := svc.AppsService.CreateCircleHub("no-d-circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{ExistingID: &legacy.ID},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	// A well-formed identity that comes after it in id order must still be
	// refreshed.
	_, _, err = svc.AppsService.CreateCircleHub("good-circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "good-circle", Policy: db.CirclePolicyFollowSet, ProviderPubkey: provider, FollowSetD: testD},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	ctx := context.Background()
	nextOffset := runSocialCacheRefresh(ctx, svc.DB, cache, nostr.NewSimplePool(ctx), 0)

	_, ok := cached(cache, provider, testD)
	assert.True(t, ok, "the well-formed identity after a bad one must still be refreshed")
	assert.Equal(t, 0, nextOffset, "sweep must wrap back to offset 0 once it reaches the end of the table")
}

func TestRunSocialCacheRefresh_SharedIdentity_RefreshedOnce(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	provider, evt := newSignedFollowSet(t, testD, []string{requesterPubkey})
	var requests atomic.Int32
	relay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		requests.Add(1)
		sendEvent(t, conn, subID, evt)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))

	first, _, err := svc.AppsService.CreateCircleHub("circle-a", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "shared", Policy: db.CirclePolicyFollowSet, ProviderPubkey: provider, FollowSetD: testD},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	firstCfg, err := svc.AppsService.GetCircleHubConfig(first.ID)
	require.NoError(t, err)
	sharedID := firstCfg.CircleIdentityID

	_, _, err = svc.AppsService.CreateCircleHub("circle-b", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{ExistingID: &sharedID},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	ctx := context.Background()
	runSocialCacheRefresh(ctx, svc.DB, cache, nostr.NewSimplePool(ctx), 0)
	// One kind:10002 discovery plus one follow-set query: a shared identity is
	// one row, so it is fetched once, not once per hub.
	assert.Equal(t, int32(2), requests.Load())
}
