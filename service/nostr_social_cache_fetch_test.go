package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// These tests run a real NIP-01 relay in-process (same technique go-nostr uses
// in its own relay_test.go) so a bug in how lokihub queries relays fails a
// test instead of hiding behind an empty relay list: the test config never
// calls config.Unlock, so GeneralRelay is empty unless a test sets it.

// respondFunc handles one REQ from a connected client and must itself write
// whatever EVENT/EOSE frames (or nothing at all) the test wants the relay to
// send back.
type respondFunc func(conn *websocket.Conn, subID string, filter nostr.Filter)

func newFakeRelay(t *testing.T, respond respondFunc) *httptest.Server {
	t.Helper()
	parser := nostr.NewMessageParser()
	srv := httptest.NewServer(&websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(conn *websocket.Conn) {
			for {
				var raw string
				if err := websocket.Message.Receive(conn, &raw); err != nil {
					return
				}
				env, err := parser.ParseMessage(raw)
				if err != nil {
					continue
				}
				req, ok := env.(*nostr.ReqEnvelope)
				if !ok {
					continue
				}
				var filter nostr.Filter
				if len(req.Filters) > 0 {
					filter = req.Filters[0]
				}
				respond(conn, req.SubscriptionID, filter)
			}
		},
	})
	t.Cleanup(srv.Close)
	return srv
}

// sendEvents writes an EVENT frame per event, then EOSE. EventEnvelope embeds
// Event anonymously, so its promoted String() resolves to Event.String() — NOT
// the ["EVENT","subid",{...}] wire format. MarshalJSON must be called
// explicitly to get the real envelope frame.
func sendEvents(t *testing.T, conn *websocket.Conn, subID string, evts ...nostr.Event) {
	t.Helper()
	for _, evt := range evts {
		env := nostr.EventEnvelope{SubscriptionID: &subID, Event: evt}
		raw, err := env.MarshalJSON()
		require.NoError(t, err)
		require.NoError(t, websocket.Message.Send(conn, string(raw)))
	}
	sendEOSE(t, conn, subID)
}

// sendEvent writes one EVENT frame followed by EOSE.
func sendEvent(t *testing.T, conn *websocket.Conn, subID string, evt nostr.Event) {
	t.Helper()
	sendEvents(t, conn, subID, evt)
}

// sendEOSE writes just an EOSE frame, mirroring a relay that has no matching event.
func sendEOSE(t *testing.T, conn *websocket.Conn, subID string) {
	t.Helper()
	eose := nostr.EOSEEnvelope(subID)
	require.NoError(t, websocket.Message.Send(conn, eose.String()))
}

// signedFollowSetEvent builds a real, signed kind-30000 event. go-nostr's
// client drops any event that fails signature verification, so a fake event
// built without a valid signature would silently vanish and a test would pass
// for the wrong reason.
func signedFollowSetEvent(t *testing.T, secretKey, d string, members []string, extra ...nostr.Tag) nostr.Event {
	t.Helper()
	tags := nostr.Tags{{"d", d}}
	for _, m := range members {
		tags = append(tags, nostr.Tag{"p", m})
	}
	tags = append(tags, extra...)
	evt := nostr.Event{
		Kind:      db.KindFollowSet,
		CreatedAt: nostr.Now(),
		Tags:      tags,
	}
	require.NoError(t, evt.Sign(secretKey))
	return evt
}

// newSignedFollowSet generates a provider and its signed set.
func newSignedFollowSet(t *testing.T, d string, members []string, extra ...nostr.Tag) (string, nostr.Event) {
	t.Helper()
	sk := nostr.GeneratePrivateKey()
	evt := signedFollowSetEvent(t, sk, d, members, extra...)
	return evt.PubKey, evt
}

// useRelayServing points the General relays at one that answers every
// follow-set query with evts, and every other query with EOSE alone.
func useRelayServing(t *testing.T, svc *tests.TestService, evts ...nostr.Event) {
	t.Helper()
	relay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		if slices.Contains(filter.Kinds, db.KindFollowSet) {
			sendEvents(t, conn, subID, evts...)
			return
		}
		sendEOSE(t, conn, subID)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))
}

func TestQueryAndStore_EventPresent_MembersParsed(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	owner, evt := newSignedFollowSet(t, testD, []string{requesterPubkey, unrelatedPubkey},
		nostr.Tag{"title", "Ada's Family Circle"}, nostr.Tag{"description", "Wallets for the family"})
	var mu sync.Mutex
	var filters []nostr.Filter
	relay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		mu.Lock()
		filters = append(filters, filter)
		mu.Unlock()
		sendEvent(t, conn, subID, evt)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))

	set, err := cache.getFollowSet(context.Background(), owner, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey))
	assert.True(t, set.IsMember(unrelatedPubkey))
	assert.Equal(t, []string{requesterPubkey, unrelatedPubkey}, set.Public)
	assert.Equal(t, "Ada's Family Circle", set.Title)
	assert.Equal(t, "Wallets for the family", set.Description)

	// The query names exactly this set: kind 30000, the provider, and its d.
	mu.Lock()
	defer mu.Unlock()
	var setFilter *nostr.Filter
	for i := range filters {
		if slices.Contains(filters[i].Kinds, db.KindFollowSet) {
			setFilter = &filters[i]
		}
	}
	require.NotNil(t, setFilter)
	assert.Equal(t, []string{owner}, setFilter.Authors)
	assert.Equal(t, []string{testD}, setFilter.Tags["d"])
}

// TestIsAuthorized_FollowSet_ReadyAfterWarmup_ColdMissStillWorks proves the
// startup-readiness gate in IsAuthorized only blocks the narrow "hub just
// started, nothing warmed yet" window — once cache.ready is set, a set with
// no cache entry yet still gets a normal cold-miss relay fetch.
func TestIsAuthorized_FollowSet_ReadyAfterWarmup_ColdMissStillWorks(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	owner, evt := newSignedFollowSet(t, testD, []string{requesterPubkey})
	useRelayServing(t, svc, evt)

	cache.ready.Store(true) // simulates StartNostrSocialCacheRefresher's initial pass having completed

	identity := makeIdentity(db.CirclePolicyFollowSet, owner)
	authorized, err := cache.IsAuthorized(context.Background(), requesterPubkey, identity, svc.DB)
	require.NoError(t, err)
	assert.True(t, authorized)
}

// relayListFor builds owner's signed NIP-65 relay list naming writeRelay.
func relayListFor(t *testing.T, sk, writeRelay string) nostr.Event {
	t.Helper()
	evt := nostr.Event{
		Kind:      nostr.KindRelayListMetadata,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"r", writeRelay, "write"}},
	}
	require.NoError(t, evt.Sign(sk))
	return evt
}

// discoverWriteRelays lets a set be found on the provider's own NIP-65 write
// relay even when none of the fixed General relays carry it.
func TestQueryAndStore_EventOnlyOnOwnersOutboxRelay_StillFound(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	sk := nostr.GeneratePrivateKey()
	setEvt := signedFollowSetEvent(t, sk, testD, []string{requesterPubkey})
	owner := setEvt.PubKey

	// outboxRelay is where the set actually lives.
	outboxRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, setEvt)
	})
	relayListEvt := relayListFor(t, sk, outboxRelay.URL)

	// baseRelay is the only configured General relay. It has no set, but does
	// have the owner's NIP-65 relay list pointing at outboxRelay.
	baseRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		if slices.Contains(filter.Kinds, nostr.KindRelayListMetadata) {
			sendEvent(t, conn, subID, relayListEvt)
			return
		}
		sendEOSE(t, conn, subID)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(baseRelay.URL))

	set, err := cache.getFollowSet(context.Background(), owner, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey), "a set living only on the owner's outbox relay must still be found")
}

// A successful outbox discovery is reused by later fetches for the same owner
// instead of re-querying kind:10002 every time.
func TestGetOutboxRelays_CachesSuccessfulDiscovery_NoRepeatedKind10002Query(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	sk := nostr.GeneratePrivateKey()
	setEvt := signedFollowSetEvent(t, sk, testD, []string{requesterPubkey})
	owner := setEvt.PubKey

	outboxRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, setEvt)
	})
	relayListEvt := relayListFor(t, sk, outboxRelay.URL)

	var kind10002Requests atomic.Int32
	baseRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		if slices.Contains(filter.Kinds, nostr.KindRelayListMetadata) {
			kind10002Requests.Add(1)
			sendEvent(t, conn, subID, relayListEvt)
			return
		}
		sendEOSE(t, conn, subID)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(baseRelay.URL))

	pool := nostr.NewSimplePool(context.Background())

	set, err := cache.queryAndStore(context.Background(), pool, owner, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey))
	assert.Equal(t, int32(1), kind10002Requests.Load(), "first fetch must discover the owner's outbox relays")

	set, err = cache.queryAndStore(context.Background(), pool, owner, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey))
	assert.Equal(t, int32(1), kind10002Requests.Load(), "second fetch must reuse the cached outbox relay list, not re-query kind:10002")
}

// An unsuccessful discovery is NOT cached, so an owner whose relay list only
// becomes reachable later is picked up on the very next fetch.
func TestGetOutboxRelays_FailedDiscovery_NotCached_RetriedNextTime(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	sk := nostr.GeneratePrivateKey()
	setEvt := signedFollowSetEvent(t, sk, testD, []string{requesterPubkey})
	owner := setEvt.PubKey

	outboxRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, setEvt)
	})
	relayListEvt := relayListFor(t, sk, outboxRelay.URL)

	var relayListPublished atomic.Bool // starts false: first discovery attempt finds nothing
	baseRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		if slices.Contains(filter.Kinds, nostr.KindRelayListMetadata) && relayListPublished.Load() {
			sendEvent(t, conn, subID, relayListEvt)
			return
		}
		sendEOSE(t, conn, subID)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(baseRelay.URL))

	pool := nostr.NewSimplePool(context.Background())

	// First call: discovery finds nothing yet and the base relay has no set.
	_, err := cache.queryAndStore(context.Background(), pool, owner, testD)
	require.ErrorIs(t, err, constants.ErrCircleMembershipUnresolved)

	relayListPublished.Store(true)

	set, err := cache.queryAndStore(context.Background(), pool, owner, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey), "a failed discovery must not be cached — the next fetch must retry")
}

// NIP-CW §Membership: a set the Hub has not found is unresolved, not empty.
// Unlike kind:3 before it, even a relay that answered "no such event" leaves
// it unresolved, and nothing is cached, so the next request asks again.
func TestQueryAndStore_RelayHasNoEvent_IsUnresolved(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()
	useRelayServing(t, svc)

	set, err := cache.getFollowSet(context.Background(), providerPubkey, testD)
	require.ErrorIs(t, err, constants.ErrCircleMembershipUnresolved)
	assert.Nil(t, set)

	_, ok := cached(cache, providerPubkey, testD)
	assert.False(t, ok, "an unfound set is not cached")
}

// A refresh that finds nothing must not blank out a previously-fetched,
// known-good set. refreshOne runs this every socialCacheRefreshInterval, so
// without this guard a single relay hiccup would revoke every real member.
func TestQueryAndStore_FailedRefresh_PreservesPreviousGoodCache(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	staleAge := socialCacheTTL + time.Second
	seedCache(cache, providerPubkey, testD, []string{requesterPubkey}, staleAge)
	before, _ := cached(cache, providerPubkey, testD)

	useRelayServing(t, svc)

	pool := nostr.NewSimplePool(context.Background())
	set, err := cache.queryAndStore(context.Background(), pool, providerPubkey, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey), "a failed refresh must keep serving the last known-good set")

	entry, _ := cached(cache, providerPubkey, testD)
	assert.True(t, entry.IsMember(requesterPubkey), "the cached entry itself must be untouched")
	assert.Equal(t, before.FetchedAt, entry.FetchedAt, "FetchedAt must still reflect the last real confirmation, not this failed attempt")
}

func TestQueryAndStore_OneRelayUnreachable_OtherRelayEventStillFound(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	owner, evt := newSignedFollowSet(t, testD, []string{requesterPubkey})
	good := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, evt)
	})
	// Nothing listens on this port: connection is refused immediately.
	deadURL := "ws://127.0.0.1:1"
	require.NoError(t, svc.Cfg.SetGeneralRelay(strings.Join([]string{deadURL, good.URL}, ",")))

	set, err := cache.getFollowSet(context.Background(), owner, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey), "one unreachable relay must not blank out a hit from another configured relay")
}

// Each relay may hold a different version. The newest one is the set,
// whichever relay answered first.
func TestQueryAndStore_RelaysDisagree_NewestVersionWins(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	sk := nostr.GeneratePrivateKey()
	old := signedFollowSetEvent(t, sk, testD, []string{unrelatedPubkey})
	old.CreatedAt -= 60
	require.NoError(t, old.Sign(sk))
	current := signedFollowSetEvent(t, sk, testD, []string{requesterPubkey})

	staleRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, old)
	})
	freshRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, current)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(strings.Join([]string{staleRelay.URL, freshRelay.URL}, ",")))

	set, err := cache.getFollowSet(context.Background(), current.PubKey, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey))
	assert.False(t, set.IsMember(unrelatedPubkey), "a member only the older version lists is not a member")
}

func TestQueryAndStore_AllRelaysUnreachable_IsUnresolved(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	require.NoError(t, svc.Cfg.SetGeneralRelay(strings.Join([]string{"ws://127.0.0.1:1", "ws://127.0.0.1:2"}, ",")))

	set, err := cache.getFollowSet(context.Background(), providerPubkey, testD)
	require.ErrorIs(t, err, constants.ErrCircleMembershipUnresolved)
	assert.Nil(t, set)
	_, ok := cached(cache, providerPubkey, testD)
	assert.False(t, ok)
}

// newSlowConnectFakeRelay is like newFakeRelay but delays the HTTP handler
// (and therefore the WebSocket upgrade handshake) by delay before serving any
// connection — a relay that is reachable but slow to complete its handshake.
func newSlowConnectFakeRelay(t *testing.T, delay time.Duration, respond respondFunc) *httptest.Server {
	t.Helper()
	parser := nostr.NewMessageParser()
	wsHandler := &websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(conn *websocket.Conn) {
			for {
				var raw string
				if err := websocket.Message.Receive(conn, &raw); err != nil {
					return
				}
				env, err := parser.ParseMessage(raw)
				if err != nil {
					continue
				}
				req, ok := env.(*nostr.ReqEnvelope)
				if !ok {
					continue
				}
				var filter nostr.Filter
				if len(req.Filters) > 0 {
					filter = req.Filters[0]
				}
				respond(conn, req.SubscriptionID, filter)
			}
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		wsHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A relay that's merely slow to complete its WebSocket handshake can't stall
// queryAndStore past RelayQueryTimeout — go-nostr's own EnsureRelay dial uses
// a hardcoded 15s timeout independent of the ctx passed in.
func TestQueryAndStore_SlowToConnectRelayHoldingOnlyEvent_BoundedByCtxDeadline(t *testing.T) {
	originalQueryTimeout, originalDiscoveryTimeout := RelayQueryTimeout, outboxDiscoveryTimeout
	RelayQueryTimeout = 200 * time.Millisecond
	outboxDiscoveryTimeout = 200 * time.Millisecond
	t.Cleanup(func() {
		RelayQueryTimeout = originalQueryTimeout
		outboxDiscoveryTimeout = originalDiscoveryTimeout
	})

	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	owner, evt := newSignedFollowSet(t, testD, []string{requesterPubkey})

	const connectDelay = 2 * time.Second
	slowRelay := newSlowConnectFakeRelay(t, connectDelay, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, evt)
	})
	fastRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEOSE(t, conn, subID)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(strings.Join([]string{slowRelay.URL, fastRelay.URL}, ",")))

	// The slow relay holds the real event and never got to answer, so the set
	// is unresolved, not empty.
	pool := nostr.NewSimplePool(context.Background())
	start := time.Now()
	set, err := cache.queryAndStore(context.Background(), pool, owner, testD)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, constants.ErrCircleMembershipUnresolved)
	assert.Nil(t, set)
	assert.Less(t, elapsed, connectDelay,
		"queryAndStore must return once RelayQueryTimeout expires instead of waiting out the slow relay's full connect time")
}

// RefreshFollowSet (the manual "Sync" button's fetch) reuses
// cache.sharedPool's already-open connections instead of cold-dialing a fresh
// ephemeral pool every call.
func TestRefreshFollowSet_ReusesSharedPool_SucceedsWhereEphemeralPoolWouldMiss(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	owner, evt := newSignedFollowSet(t, testD, []string{requesterPubkey})

	const connectDelay = 2 * time.Second
	relay := newSlowConnectFakeRelay(t, connectDelay, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		if slices.Contains(filter.Kinds, db.KindFollowSet) {
			sendEvent(t, conn, subID, evt)
			return
		}
		sendEOSE(t, conn, subID)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))

	sharedPool := nostr.NewSimplePool(context.Background())
	_, err := sharedPool.EnsureRelay(relay.URL)
	require.NoError(t, err)
	cache.sharedPool.Store(sharedPool)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	set, err := cache.RefreshFollowSet(ctx, owner, testD)
	require.NoError(t, err)
	assert.True(t, set.IsMember(requesterPubkey), "a Sync after the connection is already warm must succeed within a tight deadline")
}

// A caller-side cancellation (short HTTP deadline, aborted request, etc.)
// surfaces as an error instead of a silent, possibly-false result.
func TestQueryAndStore_CallerCtxExpires_BoundedAndReturnsError(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	relay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		<-block
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	pool := nostr.NewSimplePool(context.Background())
	done := make(chan struct{})
	var set *FollowSet
	var err error
	go func() {
		set, err = cache.queryAndStore(ctx, pool, providerPubkey, testD)
		close(done)
	}()

	select {
	case <-done:
		assert.ErrorIs(t, err, context.DeadlineExceeded, "a caller-side timeout must be surfaced as an error")
		assert.Nil(t, set)
	case <-time.After(3 * time.Second):
		t.Fatal("queryAndStore did not return within a bounded time for an unresponsive relay")
	}

	_, ok := cached(cache, providerPubkey, testD)
	assert.False(t, ok, "a caller-side cancellation must not cache anything")
}

// RelayQueryTimeout itself (lowered here for speed) cuts the query short while
// the caller's own ctx has no deadline. The relay never answered, so the set
// is unresolved and nothing is cached.
func TestQueryAndStore_RelayQueryTimeoutExpires_CallerCtxStillValid(t *testing.T) {
	original := RelayQueryTimeout
	RelayQueryTimeout = 300 * time.Millisecond
	t.Cleanup(func() { RelayQueryTimeout = original })

	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	relay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		<-block
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))

	pool := nostr.NewSimplePool(context.Background())
	set, err := cache.queryAndStore(context.Background(), pool, providerPubkey, testD)

	require.ErrorIs(t, err, constants.ErrCircleMembershipUnresolved)
	assert.Nil(t, set)

	_, ok := cached(cache, providerPubkey, testD)
	assert.False(t, ok, "an unresolved set is not cached")
}
