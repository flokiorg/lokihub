package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/flokiorg/lokihub/db"
)

// The tests in nostr_social_cache_test.go never actually talk to a relay:
// the test config never calls config.Unlock (see keys.Init), so GeneralRelay
// is never seeded and cfg.GetGeneralRelayUrls() is always empty there. That
// makes pool.QuerySingle query zero relays and vacuously "miss" every time —
// none of that path (real REQ/EVENT/EOSE exchange, partial relay failure,
// timeout bounding) is exercised. These tests fill that gap by running a real
// NIP-01 relay in-process (same technique go-nostr uses in its own
// relay_test.go) so a bug in how lokihub queries relays would actually fail a
// test instead of hiding behind an empty relay list.

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

// sendEvent writes an EVENT frame followed by EOSE, mirroring a relay that
// has the requested kind:3 event. EventEnvelope embeds Event anonymously, so
// its promoted String() resolves to Event.String() (a bare event object) —
// NOT the ["EVENT","subid",{...}] wire format. MarshalJSON must be called
// explicitly to get the real envelope frame.
func sendEvent(t *testing.T, conn *websocket.Conn, subID string, evt nostr.Event) {
	t.Helper()
	env := nostr.EventEnvelope{SubscriptionID: &subID, Event: evt}
	raw, err := env.MarshalJSON()
	require.NoError(t, err)
	require.NoError(t, websocket.Message.Send(conn, string(raw)))
	sendEOSE(t, conn, subID)
}

// sendEOSE writes just an EOSE frame, mirroring a relay that has no matching event.
func sendEOSE(t *testing.T, conn *websocket.Conn, subID string) {
	t.Helper()
	eose := nostr.EOSEEnvelope(subID)
	require.NoError(t, websocket.Message.Send(conn, eose.String()))
}

// signedContactListEvent builds a real, signed kind:3 event for ownerPubkey
// following each of contacts — go-nostr's client drops any event that fails
// signature verification (see relay.go's EventEnvelope handling), so a fake
// event built without a valid signature would silently vanish and the test
// would pass for the wrong reason.
func signedContactListEvent(t *testing.T, secretKey, ownerPubkey string, contacts []string) nostr.Event {
	t.Helper()
	tags := make(nostr.Tags, len(contacts))
	for i, c := range contacts {
		tags[i] = nostr.Tag{"p", c}
	}
	evt := nostr.Event{
		PubKey:    ownerPubkey,
		Kind:      3,
		CreatedAt: nostr.Now(),
		Tags:      tags,
	}
	require.NoError(t, evt.Sign(secretKey))
	return evt
}

func TestQueryAndStore_EventPresent_ContactsParsed(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	sk := nostr.GeneratePrivateKey()
	owner, err := nostr.GetPublicKey(sk)
	require.NoError(t, err)
	evt := signedContactListEvent(t, sk, owner, []string{requesterPubkey, unrelatedPubkey})

	relay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, evt)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))

	contacts, err := cache.getContactList(context.Background(), owner)
	require.NoError(t, err)
	assert.Contains(t, contacts, requesterPubkey)
	assert.Contains(t, contacts, unrelatedPubkey)
}

// TestIsAuthorized_Following_ReadyAfterWarmup_ColdMissStillWorks proves the
// startup-readiness gate in IsAuthorized only blocks the narrow "hub just
// started, nothing warmed yet" window — once cache.ready is set (as
// StartNostrSocialCacheRefresher does after its initial pass), a provider
// with no cache entry yet still gets the normal, latency-tolerant cold-miss
// relay fetch instead of an unconditional ErrSocialCacheWarmingUp.
func TestIsAuthorized_Following_ReadyAfterWarmup_ColdMissStillWorks(t *testing.T) {
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

	cache.ready.Store(true) // simulates StartNostrSocialCacheRefresher's initial pass having completed

	identity := makeIdentity(db.CirclePolicyFollowing, owner)
	authorized, err := cache.IsAuthorized(context.Background(), requesterPubkey, identity, svc.DB)
	require.NoError(t, err)
	assert.True(t, authorized)
}

// TestQueryAndStore_EventOnlyOnOwnersOutboxRelay_StillFound proves
// discoverWriteRelays lets a kind:3 event be found on an owner's own NIP-65
// write relay even when none of the fixed General relays carry it.
func TestQueryAndStore_EventOnlyOnOwnersOutboxRelay_StillFound(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	sk := nostr.GeneratePrivateKey()
	owner, err := nostr.GetPublicKey(sk)
	require.NoError(t, err)
	contactEvt := signedContactListEvent(t, sk, owner, []string{requesterPubkey})

	// outboxRelay is where the owner's kind:3 actually lives. It never
	// receives a kind:10002 request in this test (discovery only queries the
	// base relay list), so it only needs to answer kind:3.
	outboxRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, contactEvt)
	})

	// baseRelay is the only configured General relay. It has no kind:3 for
	// this owner, but does have the owner's NIP-65 relay list pointing at
	// outboxRelay as a write relay.
	relayListEvt := nostr.Event{
		PubKey:    owner,
		Kind:      nostr.KindRelayListMetadata,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"r", outboxRelay.URL, "write"}},
	}
	require.NoError(t, relayListEvt.Sign(sk))

	baseRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		if slices.Contains(filter.Kinds, nostr.KindRelayListMetadata) {
			sendEvent(t, conn, subID, relayListEvt)
			return
		}
		sendEOSE(t, conn, subID) // no kind:3 here
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(baseRelay.URL))

	contacts, err := cache.getContactList(context.Background(), owner)
	require.NoError(t, err)
	assert.Contains(t, contacts, requesterPubkey, "kind:3 living only on the owner's outbox relay must still be found")
}

// TestGetOutboxRelays_CachesSuccessfulDiscovery_NoRepeatedKind10002Query
// proves a successful outbox discovery is reused by later fetches for the
// same owner (every socialCacheRefreshInterval tick, or a repeated manual
// Sync) instead of re-querying kind:10002 every single time.
func TestGetOutboxRelays_CachesSuccessfulDiscovery_NoRepeatedKind10002Query(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	sk := nostr.GeneratePrivateKey()
	owner, err := nostr.GetPublicKey(sk)
	require.NoError(t, err)
	contactEvt := signedContactListEvent(t, sk, owner, []string{requesterPubkey})

	outboxRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, contactEvt)
	})
	relayListEvt := nostr.Event{
		PubKey:    owner,
		Kind:      nostr.KindRelayListMetadata,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"r", outboxRelay.URL, "write"}},
	}
	require.NoError(t, relayListEvt.Sign(sk))

	var kind10002Requests atomic.Int32
	baseRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		if slices.Contains(filter.Kinds, nostr.KindRelayListMetadata) {
			kind10002Requests.Add(1)
			sendEvent(t, conn, subID, relayListEvt)
			return
		}
		sendEOSE(t, conn, subID) // base relay never has the kind:3 itself
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(baseRelay.URL))

	pool := nostr.NewSimplePool(context.Background())

	// First call: cold miss, must discover via kind:10002.
	contacts, err := cache.queryAndStore(context.Background(), pool, owner)
	require.NoError(t, err)
	assert.Contains(t, contacts, requesterPubkey)
	assert.Equal(t, int32(1), kind10002Requests.Load(), "first fetch must discover the owner's outbox relays")

	// Second call (e.g. the next periodic refresh tick, or a repeated manual
	// Sync): must reuse the cached outbox relay list instead of querying
	// kind:10002 again.
	contacts, err = cache.queryAndStore(context.Background(), pool, owner)
	require.NoError(t, err)
	assert.Contains(t, contacts, requesterPubkey)
	assert.Equal(t, int32(1), kind10002Requests.Load(), "second fetch must reuse the cached outbox relay list, not re-query kind:10002")
}

// TestGetOutboxRelays_FailedDiscovery_NotCached_RetriedNextTime proves an
// unsuccessful discovery attempt is NOT cached — unlike a successful one —
// so an owner whose relay list only becomes reachable later (e.g. a relay
// recovers, or the owner publishes one for the first time) is picked up on
// the very next fetch instead of being permanently stuck with "discovery
// already tried and failed".
func TestGetOutboxRelays_FailedDiscovery_NotCached_RetriedNextTime(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	sk := nostr.GeneratePrivateKey()
	owner, err := nostr.GetPublicKey(sk)
	require.NoError(t, err)
	contactEvt := signedContactListEvent(t, sk, owner, []string{requesterPubkey})

	outboxRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, contactEvt)
	})
	relayListEvt := nostr.Event{
		PubKey:    owner,
		Kind:      nostr.KindRelayListMetadata,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"r", outboxRelay.URL, "write"}},
	}
	require.NoError(t, relayListEvt.Sign(sk))

	var relayListPublished atomic.Bool // starts false: first discovery attempt finds nothing
	baseRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		if slices.Contains(filter.Kinds, nostr.KindRelayListMetadata) {
			if relayListPublished.Load() {
				sendEvent(t, conn, subID, relayListEvt)
			} else {
				sendEOSE(t, conn, subID)
			}
			return
		}
		sendEOSE(t, conn, subID) // base relay never has the kind:3 itself
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(baseRelay.URL))

	pool := nostr.NewSimplePool(context.Background())

	// First call: discovery finds nothing yet, base relay also has no kind:3
	// — a genuine miss.
	contacts, err := cache.queryAndStore(context.Background(), pool, owner)
	require.NoError(t, err)
	assert.Empty(t, contacts)

	relayListPublished.Store(true)

	// Second call must retry discovery rather than treat the earlier empty
	// result as final, and this time find the owner's outbox relay.
	contacts, err = cache.queryAndStore(context.Background(), pool, owner)
	require.NoError(t, err)
	assert.Contains(t, contacts, requesterPubkey, "a failed discovery must not be cached — the next fetch must retry and can succeed once the owner's relay list becomes discoverable")
}

func TestQueryAndStore_RelayHasNoEvent_ReturnsEmptyWithoutError(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	// A relay that is reachable and answers correctly (EOSE, no EVENT) — the
	// exact wire behavior behind the "No kind:3 event found for pubkey"
	// warning when the owner genuinely has no kind:3 on this relay.
	relay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEOSE(t, conn, subID)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))

	contacts, err := cache.getContactList(context.Background(), requesterPubkey)
	require.NoError(t, err)
	assert.Empty(t, contacts)

	entry, ok := cache.cache[requesterPubkey]
	require.True(t, ok, "a reachable relay with no event must still populate the cache (with an empty set)")
	assert.WithinDuration(t, time.Now(), entry.fetchedAt, 5*time.Second)
}

// TestQueryAndStore_FailedRefresh_PreservesPreviousGoodCache guards against a
// real bug: a refresh that comes back empty (relay unreachable, or timed out —
// indistinguishable from "genuinely no event" per
// TestQueryAndStore_AllRelaysUnreachable_MissLooksLikeNoEvent) must not
// silently blank out a previously-fetched, known-good contact list.
// refreshOne runs this every socialCacheRefreshInterval for every
// following-policy provider, so without this guard a single transient relay
// hiccup would revoke every real follower's access until the next successful
// refresh.
func TestQueryAndStore_FailedRefresh_PreservesPreviousGoodCache(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	staleAge := socialCacheTTL + time.Second
	seedCache(cache, providerPubkey, []string{requesterPubkey}, staleAge)
	before := cache.cache[providerPubkey].fetchedAt

	// Configured relay is reachable but this refresh attempt finds nothing —
	// e.g. a transient hiccup, not an actual revocation by the owner.
	relay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEOSE(t, conn, subID)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))

	pool := nostr.NewSimplePool(context.Background())
	contacts, err := cache.queryAndStore(context.Background(), pool, providerPubkey)
	require.NoError(t, err)
	assert.Contains(t, contacts, requesterPubkey, "a failed refresh must keep serving the last known-good list")

	entry := cache.cache[providerPubkey]
	assert.Contains(t, entry.pubkeys, requesterPubkey, "the cached entry itself must be untouched, not replaced with an empty one")
	assert.Equal(t, before, entry.fetchedAt, "fetchedAt must still reflect the last real confirmation, not this failed attempt")
}

func TestQueryAndStore_OneRelayUnreachable_OtherRelayEventStillFound(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	sk := nostr.GeneratePrivateKey()
	owner, err := nostr.GetPublicKey(sk)
	require.NoError(t, err)
	evt := signedContactListEvent(t, sk, owner, []string{requesterPubkey})

	good := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, evt)
	})
	// Nothing listens on this port: connection is refused immediately,
	// simulating one dead/misconfigured relay in the configured list.
	deadURL := "ws://127.0.0.1:1"

	require.NoError(t, svc.Cfg.SetGeneralRelay(strings.Join([]string{deadURL, good.URL}, ",")))

	contacts, err := cache.getContactList(context.Background(), owner)
	require.NoError(t, err)
	assert.Contains(t, contacts, requesterPubkey, "one unreachable relay must not blank out a hit from another configured relay")
}

func TestQueryAndStore_AllRelaysUnreachable_MissLooksLikeNoEvent(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	// Documents a real observability gap: a relay-connection failure and a
	// relay that is reachable but genuinely has no event both surface as the
	// exact same outcome here (empty set, nil error) — and thus the same
	// "No kind:3 event found for pubkey" warning in queryAndStore. From the
	// warning alone there is no way to tell "the contact has no kind:3" apart
	// from "every configured relay was unreachable".
	require.NoError(t, svc.Cfg.SetGeneralRelay(strings.Join([]string{"ws://127.0.0.1:1", "ws://127.0.0.1:2"}, ",")))

	contacts, err := cache.getContactList(context.Background(), requesterPubkey)
	require.NoError(t, err)
	assert.Empty(t, contacts)
}

// newSlowConnectFakeRelay is like newFakeRelay but delays the HTTP handler
// (and therefore the WebSocket upgrade handshake) by delay before serving any
// connection — simulating a relay that is reachable but slow to complete its
// handshake, as opposed to newFakeRelay's instantly-answering relay or a dead
// port that fails to connect at all.
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

// TestQueryAndStore_SlowToConnectRelayHoldingOnlyEvent_BoundedByCtxDeadline
// proves a relay that's merely slow to complete its WebSocket handshake (as
// opposed to unreachable, or reachable-but-silent after connecting) can't
// stall queryAndStore past RelayQueryTimeout — go-nostr's own EnsureRelay
// dial uses a hardcoded 15s timeout independent of the ctx passed to
// QuerySingle, which queryRelaysBounded compensates for.
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

	sk := nostr.GeneratePrivateKey()
	owner, err := nostr.GetPublicKey(sk)
	require.NoError(t, err)
	evt := signedContactListEvent(t, sk, owner, []string{requesterPubkey})

	// slowRelay actually has the event, but its handshake takes longer than
	// RelayQueryTimeout above — mirrors relay.damus.io in the live reproduction.
	const connectDelay = 2 * time.Second
	slowRelay := newSlowConnectFakeRelay(t, connectDelay, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, evt)
	})
	// fastRelay connects immediately but has nothing — mirrors relay.primal.net/
	// relay.ohstr.com in the live reproduction, which answered instantly with EOSE.
	fastRelay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEOSE(t, conn, subID)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(strings.Join([]string{slowRelay.URL, fastRelay.URL}, ",")))

	// Caller's own ctx has no deadline — RelayQueryTimeout alone must bound
	// this call, and (being a genuine relay-side miss, not caller cancellation)
	// must still succeed with an empty, cached result rather than an error.
	pool := nostr.NewSimplePool(context.Background())
	start := time.Now()
	contacts, err := cache.queryAndStore(context.Background(), pool, owner)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Empty(t, contacts, "the slow relay's real event still arrives too late to be seen — bounding the call doesn't make the event appear, it only stops the caller from hanging")
	assert.Less(t, elapsed, connectDelay,
		"queryAndStore must return once RelayQueryTimeout expires instead of waiting out the slow relay's full connect time")
}

// TestWarmFollowingCache_ReusesSharedPool_SucceedsWhereEphemeralPoolWouldMiss
// proves WarmFollowingCache (the manual "Sync" button's underlying fetch)
// reuses cache.sharedPool's already-open connections instead of cold-dialing
// a fresh ephemeral pool every call.
func TestWarmFollowingCache_ReusesSharedPool_SucceedsWhereEphemeralPoolWouldMiss(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	sk := nostr.GeneratePrivateKey()
	owner, err := nostr.GetPublicKey(sk)
	require.NoError(t, err)
	evt := signedContactListEvent(t, sk, owner, []string{requesterPubkey})

	const connectDelay = 2 * time.Second
	relay := newSlowConnectFakeRelay(t, connectDelay, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		sendEvent(t, conn, subID, evt)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))

	sharedPool := nostr.NewSimplePool(context.Background())
	// Simulates the connection already being warm — e.g. from start.go's
	// startup warm-up loop, or from any earlier successful fetch on this
	// same pool — before this specific WarmFollowingCache call happens.
	_, err = sharedPool.EnsureRelay(relay.URL)
	require.NoError(t, err)
	cache.sharedPool.Store(sharedPool)

	// Shorter than connectDelay: a fresh ephemeral pool would miss this
	// deadline entirely (as proven above), but the connection is already
	// open on sharedPool, so EnsureRelay returns immediately here.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	contacts, err := cache.WarmFollowingCache(ctx, owner)
	require.NoError(t, err)
	assert.Contains(t, contacts, requesterPubkey, "a Sync click after the connection is already warm must succeed within a tight deadline, not just the very first one at startup")
}

// TestQueryAndStore_CallerCtxExpires_BoundedAndReturnsError proves a
// caller-side cancellation (short HTTP deadline, aborted request, etc.)
// surfaces as an error instead of a silent, possibly-false empty result —
// contrast with TestQueryAndStore_RelayQueryTimeoutExpires_CallerCtxStillValid.
func TestQueryAndStore_CallerCtxExpires_BoundedAndReturnsError(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	// Relay accepts the REQ but never answers — without a bounded context this
	// would hang for as long as the relay stays connected.
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	relay := newFakeRelay(t, func(conn *websocket.Conn, subID string, filter nostr.Filter) {
		<-block
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))

	// Far shorter than RelayQueryTimeout, so it's unambiguously the caller's
	// own ctx that runs out here, not RelayQueryTimeout.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	pool := nostr.NewSimplePool(context.Background())
	done := make(chan struct{})
	var contacts map[string]struct{}
	var err error
	go func() {
		contacts, err = cache.queryAndStore(ctx, pool, requesterPubkey)
		close(done)
	}()

	select {
	case <-done:
		assert.ErrorIs(t, err, context.DeadlineExceeded, "a caller-side timeout must be surfaced as an error, not silently reported as an empty contact list")
		assert.Nil(t, contacts)
	case <-time.After(3 * time.Second):
		t.Fatal("queryAndStore did not return within a bounded time for an unresponsive relay")
	}

	_, cached := cache.cache[requesterPubkey]
	assert.False(t, cached, "a caller-side cancellation must not cement a false-empty cache entry")
}

// TestQueryAndStore_RelayQueryTimeoutExpires_CallerCtxStillValid is the
// contrast to the test above: RelayQueryTimeout itself (lowered here for
// speed) cuts the query short while the caller's own ctx has no deadline —
// a genuine relay-side miss, so it must log/cache normally, not error.
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
	contacts, err := cache.queryAndStore(context.Background(), pool, requesterPubkey)

	require.NoError(t, err, "a genuine RelayQueryTimeout expiry (caller ctx still valid) must not surface as an error — same contract as before this distinction existed")
	assert.Empty(t, contacts)

	entry, ok := cache.cache[requesterPubkey]
	assert.True(t, ok, "a genuine relay-side miss must still populate the cache, unlike a caller-side cancellation")
	assert.WithinDuration(t, time.Now(), entry.fetchedAt, 5*time.Second)
}
