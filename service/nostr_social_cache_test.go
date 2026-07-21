package service

import (
	"context"
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
// now validates ProviderPubkey's format (see the create_circle_wallet
// security-audit round), so a short opaque placeholder like the old "aaa1"
// no longer round-trips through CreateCircleHub.
const (
	providerPubkey  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	requesterPubkey = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	unrelatedPubkey = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// seedCache injects a contact list directly into the cache, bypassing relay calls.
func seedCache(s *nostrSocialCache, ownerPubkey string, contacts []string, age time.Duration) {
	pubkeys := make(map[string]struct{}, len(contacts))
	for _, p := range contacts {
		pubkeys[p] = struct{}{}
	}
	s.mu.Lock()
	s.cache[ownerPubkey] = &contactEntry{
		pubkeys:   pubkeys,
		fetchedAt: time.Now().Add(-age),
	}
	s.mu.Unlock()
}

func newSocialCacheForTest(t *testing.T) (*nostrSocialCache, *tests.TestService) {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	cache := NewNostrSocialCache(svc.Cfg)
	return cache, svc
}

// makeIdentity builds a CircleIdentity for social-graph policy tests where no
// DB lookup is needed (ID is irrelevant).
func makeIdentity(policy, provPubkey string) *db.CircleIdentity {
	return &db.CircleIdentity{
		Policy:         policy,
		ProviderPubkey: provPubkey,
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
// Following policy: provider's contact list must contain requesterPubkey
// ---------------------------------------------------------------------------

func TestIsAuthorized_Following_Allowed(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	seedCache(cache, providerPubkey, []string{requesterPubkey}, 0)

	identity := makeIdentity(db.CirclePolicyFollowing, providerPubkey)
	ok, err := cache.IsAuthorized(context.TODO(), requesterPubkey, identity, svc.DB)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestIsAuthorized_Following_Denied(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	seedCache(cache, providerPubkey, []string{unrelatedPubkey}, 0)

	identity := makeIdentity(db.CirclePolicyFollowing, providerPubkey)
	ok, err := cache.IsAuthorized(context.TODO(), requesterPubkey, identity, svc.DB)
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestIsAuthorized_Following_NotReady_ReturnsWarmingUp covers the hub-still-
// starting-up case: a fresh cache (ready defaults to false until
// StartNostrSocialCacheRefresher's initial pass completes) with no entry yet
// for this provider must refuse fast with ErrSocialCacheWarmingUp instead of
// attempting a relay fetch — GetGeneralRelayUrls() is empty in this test env
// (see newSocialCacheForTest), so if this test did fall through to a relay
// fetch it would still resolve quickly to "not authorized", making that bug
// easy to miss without asserting the specific error.
func TestIsAuthorized_Following_NotReady_ReturnsWarmingUp(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	_, ok := cache.cache[providerPubkey]
	require.False(t, ok, "precondition: nothing cached yet for this pubkey")

	identity := makeIdentity(db.CirclePolicyFollowing, providerPubkey)
	authorized, err := cache.IsAuthorized(context.TODO(), requesterPubkey, identity, svc.DB)
	assert.ErrorIs(t, err, constants.ErrSocialCacheWarmingUp)
	assert.False(t, authorized)

	_, ok = cache.cache[providerPubkey]
	assert.False(t, ok, "the readiness gate must not itself populate the cache")
}

// TestIsAuthorized_Following_NotReady_ButCached_EvaluatesNormally proves the
// gate only blocks a true "never fetched" case: once this specific provider
// has any cached entry, the overall ready flag doesn't matter — matches
// TestIsAuthorized_Following_Allowed/Denied above, which also run with
// ready == false, just seeded.
func TestIsAuthorized_Following_NotReady_ButCached_EvaluatesNormally(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	seedCache(cache, providerPubkey, []string{requesterPubkey}, 0)

	identity := makeIdentity(db.CirclePolicyFollowing, providerPubkey)
	authorized, err := cache.IsAuthorized(context.TODO(), requesterPubkey, identity, svc.DB)
	require.NoError(t, err)
	assert.True(t, authorized)
}

// TestIsAuthorized_Allowlist_NotReady_Unaffected proves the readiness gate is
// scoped to CirclePolicyFollowing only — allowlist authorization (a plain DB
// query) must never be gated on the Nostr social cache's warm-up state.
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
// requester's own self-published contact list, which provides no real access
// control (anyone can fabricate one for free). IsAuthorized must deny by
// default for any policy value it doesn't recognize.
// ---------------------------------------------------------------------------

func TestIsAuthorized_UnsupportedPolicy_Denied(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	identity := makeIdentity("followers", providerPubkey)
	ok, err := cache.IsAuthorized(context.TODO(), requesterPubkey, identity, svc.DB)
	require.NoError(t, err)
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// Cache behaviour
// ---------------------------------------------------------------------------

func TestGetContactList_CacheHit_NoRefetch(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	seedCache(cache, requesterPubkey, []string{providerPubkey}, 0)
	before := cache.cache[requesterPubkey].fetchedAt

	contacts, err := cache.getContactList(context.TODO(), requesterPubkey)
	require.NoError(t, err)
	assert.Contains(t, contacts, providerPubkey)

	after := cache.cache[requesterPubkey].fetchedAt
	assert.Equal(t, before, after, "cache hit must not update fetchedAt")
}

func TestGetContactList_StaleEntry_ServedWithoutRefetch(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	seedCache(cache, requesterPubkey, []string{providerPubkey}, socialCacheTTL+time.Second)
	before := cache.cache[requesterPubkey].fetchedAt

	contacts, err := cache.getContactList(context.TODO(), requesterPubkey)
	require.NoError(t, err)
	assert.Contains(t, contacts, providerPubkey)

	after := cache.cache[requesterPubkey].fetchedAt
	assert.Equal(t, before, after, "a stale-but-present entry must be served as-is, never refetched on the request path")
}

func TestGetContactList_ColdMiss_TriggersFetch(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	_, ok := cache.cache[requesterPubkey]
	require.False(t, ok, "precondition: nothing cached yet for this pubkey")

	_, err := cache.getContactList(context.TODO(), requesterPubkey)
	require.NoError(t, err)

	entry, ok := cache.cache[requesterPubkey]
	require.True(t, ok, "a true cold miss must populate the cache")
	assert.WithinDuration(t, time.Now(), entry.fetchedAt, 5*time.Second)
}

func TestWarmFollowingCache_PopulatesCache(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	contacts, err := cache.WarmFollowingCache(context.TODO(), providerPubkey)
	require.NoError(t, err)

	entry, ok := cache.cache[providerPubkey]
	require.True(t, ok, "WarmFollowingCache must populate the cache for the given provider")
	assert.Equal(t, entry.pubkeys, contacts, "the returned set must be the same one stored in the cache")
}

// TestRefreshOne_UpdatesStaleEntry_OnSuccessfulFetch exercises a refresh that
// actually confirms something against a real relay. A refresh that finds
// nothing (relay unreachable, or genuinely no event) must NOT be treated as
// having updated the entry — see
// TestQueryAndStore_FailedRefresh_PreservesPreviousGoodCache in
// nostr_social_cache_fetch_test.go, which covers that failure path directly.
func TestRefreshOne_UpdatesStaleEntry_OnSuccessfulFetch(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	sk := nostr.GeneratePrivateKey()
	owner, err := nostr.GetPublicKey(sk)
	require.NoError(t, err)
	evt := signedContactListEvent(t, sk, owner, []string{requesterPubkey})

	relay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, evt)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))

	seedCache(cache, owner, []string{unrelatedPubkey}, socialCacheTTL+time.Second)
	before := cache.cache[owner].fetchedAt

	ctx := context.Background()
	pool := nostr.NewSimplePool(ctx)
	err = cache.refreshOne(ctx, pool, owner)
	require.NoError(t, err)

	after := cache.cache[owner].fetchedAt
	assert.True(t, after.After(before), "refreshOne must replace a stale entry with a fresh fetch once the relay actually confirms one")
	assert.Contains(t, cache.cache[owner].pubkeys, requesterPubkey)
}

// ---------------------------------------------------------------------------
// PeekContactCount / ContactCount
// ---------------------------------------------------------------------------

func TestPeekContactCount_CacheMiss_NeverFetches(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	count, ok := cache.PeekContactCount(requesterPubkey)
	assert.False(t, ok, "a true cache miss must report ok=false, never trigger a fetch")
	assert.Equal(t, 0, count)

	_, stillMissing := cache.cache[requesterPubkey]
	assert.False(t, stillMissing, "PeekContactCount must never populate the cache")
}

func TestPeekContactCount_CacheHit_ReturnsCount(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	seedCache(cache, providerPubkey, []string{requesterPubkey, unrelatedPubkey}, 0)

	count, ok := cache.PeekContactCount(providerPubkey)
	require.True(t, ok)
	assert.Equal(t, 2, count)
}

func TestContactCount_ColdFetch(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	count, err := cache.ContactCount(context.TODO(), requesterPubkey)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, 0)

	_, ok := cache.cache[requesterPubkey]
	assert.True(t, ok, "ContactCount may cold-fetch, populating the cache")
}

// ---------------------------------------------------------------------------
// Periodic background refresh
// ---------------------------------------------------------------------------

func TestRunSocialCacheRefresh_SkipsBadProvider_ContinuesSweep(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	// A following-policy identity with no ProviderPubkey configured must be
	// skipped rather than aborting the sweep.
	_, _, err := svc.AppsService.CreateCircleHub("no-pubkey-circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "no-pubkey-circle", Policy: db.CirclePolicyFollowing, ProviderPubkey: ""},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	// A well-formed following-policy identity that comes after it in id order
	// must still be refreshed.
	_, _, err = svc.AppsService.CreateCircleHub("good-circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "good-circle", Policy: db.CirclePolicyFollowing, ProviderPubkey: providerPubkey},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)

	ctx := context.Background()
	pool := nostr.NewSimplePool(ctx)
	nextOffset := runSocialCacheRefresh(ctx, svc.DB, cache, pool, 0)

	_, ok := cache.cache[providerPubkey]
	assert.True(t, ok, "the well-formed identity after a bad one must still be refreshed")
	assert.Equal(t, 0, nextOffset, "sweep must wrap back to offset 0 once it reaches the end of the table")
}

func TestRunSocialCacheRefresh_SharedIdentity_RefreshedOnce(t *testing.T) {
	_, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	first, _, err := svc.AppsService.CreateCircleHub("circle-a", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "shared", Policy: db.CirclePolicyFollowing, ProviderPubkey: providerPubkey},
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

	// Even though two circle_hub apps reference this identity, the
	// following-policy identity table itself has exactly one row for it.
	identities, err := svc.AppsService.ListCircleIdentities()
	require.NoError(t, err)
	matching := 0
	for _, i := range identities {
		if i.ID == sharedID {
			matching++
		}
	}
	assert.Equal(t, 1, matching, "a shared identity must appear once, not once per referencing provider")
}
