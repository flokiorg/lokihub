package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// These tests pin NIP-CW §Membership and §Hidden Members, one rule each.

// hiddenEntry encrypts member the way a provider does for a hub whose inbox
// key is inboxPub: NIP-44 v2 under the provider/inbox conversation key.
func hiddenEntry(t *testing.T, providerSK, inboxPub, member string) nostr.Tag {
	t.Helper()
	key, err := nip44.GenerateConversationKey(inboxPub, providerSK)
	require.NoError(t, err)
	payload, err := nip44.Encrypt(member, key)
	require.NoError(t, err)
	return nostr.Tag{"hidden_p", payload}
}

func newKeypair(t *testing.T) (string, string) {
	t.Helper()
	sk := nostr.GeneratePrivateKey()
	pk, err := nostr.GetPublicKey(sk)
	require.NoError(t, err)
	return sk, pk
}

// cacheWithInbox is a cache whose hub holds inboxSK as its inbox key.
func cacheWithInbox(t *testing.T, inboxSK string) *nostrSocialCache {
	t.Helper()
	cache, svc := newSocialCacheForTest(t)
	t.Cleanup(svc.Remove)
	cache.inboxKey = func() (string, error) { return inboxSK, nil }
	return cache
}

// --- Which event ---

func TestSelectFollowSetEvent_OnlyTheProvidersSignedSetWithThisD(t *testing.T) {
	providerSK, provider := newKeypair(t)
	otherSK, _ := newKeypair(t)

	good := signedFollowSetEvent(t, providerSK, testD, []string{requesterPubkey})

	otherAuthor := signedFollowSetEvent(t, otherSK, testD, []string{unrelatedPubkey})
	otherAuthor.CreatedAt = good.CreatedAt + 10
	require.NoError(t, otherAuthor.Sign(otherSK))

	// Claims the provider's pubkey but is signed by someone else.
	forged := otherAuthor
	forged.PubKey = provider
	forged.ID = forged.GetID()

	otherD := signedFollowSetEvent(t, providerSK, "work", []string{unrelatedPubkey})
	otherD.CreatedAt = good.CreatedAt + 10
	require.NoError(t, otherD.Sign(providerSK))

	otherKind := signedFollowSetEvent(t, providerSK, testD, []string{unrelatedPubkey})
	otherKind.Kind = 30001
	otherKind.CreatedAt = good.CreatedAt + 10
	require.NoError(t, otherKind.Sign(providerSK))

	selected := selectFollowSetEvent([]nostr.Event{otherAuthor, forged, otherD, otherKind, good}, provider, testD)
	require.NotNil(t, selected)
	assert.Equal(t, good.ID, selected.ID, "an event from any other author, d or kind must be ignored, whatever it claims")
}

func TestSelectFollowSetEvent_NewestWins_TieGoesToLowestID(t *testing.T) {
	providerSK, provider := newKeypair(t)

	older := signedFollowSetEvent(t, providerSK, testD, []string{unrelatedPubkey})
	older.CreatedAt -= 100
	require.NoError(t, older.Sign(providerSK))
	newer := signedFollowSetEvent(t, providerSK, testD, []string{requesterPubkey})

	selected := selectFollowSetEvent([]nostr.Event{newer, older}, provider, testD)
	assert.Equal(t, newer.ID, selected.ID)

	a := signedFollowSetEvent(t, providerSK, testD, []string{requesterPubkey}, nostr.Tag{"title", "a"})
	b := signedFollowSetEvent(t, providerSK, testD, []string{requesterPubkey}, nostr.Tag{"title", "b"})
	b.CreatedAt = a.CreatedAt
	require.NoError(t, b.Sign(providerSK))
	lowest := a
	if b.ID < a.ID {
		lowest = b
	}
	assert.Equal(t, lowest.ID, selectFollowSetEvent([]nostr.Event{a, b}, provider, testD).ID)
	assert.Equal(t, lowest.ID, selectFollowSetEvent([]nostr.Event{b, a}, provider, testD).ID)
}

// The signature covers the content, not the id field, so a relay can resend a
// genuine version under a forged low id. That copy must not win the same-second
// tie: the version that really has the lowest id does.
func TestSelectFollowSetEvent_ForgedIDCannotWinTheTie(t *testing.T) {
	providerSK, provider := newKeypair(t)

	a := signedFollowSetEvent(t, providerSK, testD, []string{requesterPubkey}, nostr.Tag{"title", "a"})
	b := signedFollowSetEvent(t, providerSK, testD, []string{unrelatedPubkey}, nostr.Tag{"title", "b"})
	b.CreatedAt = a.CreatedAt
	require.NoError(t, b.Sign(providerSK))
	winner, loser := a, b
	if b.ID < a.ID {
		winner, loser = b, a
	}

	forged := loser
	forged.ID = "0000000000000000000000000000000000000000000000000000000000000000"
	ok, err := forged.CheckSignature()
	require.NoError(t, err)
	require.True(t, ok, "the signature still verifies, which is why the id must be checked too")

	selected := selectFollowSetEvent([]nostr.Event{forged, winner}, provider, testD)
	require.NotNil(t, selected)
	assert.Equal(t, winner.ID, selected.ID)

	assert.Nil(t, selectFollowSetEvent([]nostr.Event{forged}, provider, testD), "an event whose id doesn't match its content is not a version at all")
}

// --- Which members ---

func TestParseFollowSet_PublicAndHiddenMembers(t *testing.T) {
	providerSK, _ := newKeypair(t)
	inboxSK, inboxPub := newKeypair(t)
	_, hidden := newKeypair(t)

	evt := signedFollowSetEvent(t, providerSK, testD, []string{requesterPubkey},
		hiddenEntry(t, providerSK, inboxPub, hidden))

	set := parseFollowSet(&evt, testD, inboxSK)
	assert.Equal(t, []string{requesterPubkey}, set.Public)
	assert.Equal(t, []string{hidden}, set.Hidden)
	assert.True(t, set.IsMember(requesterPubkey))
	assert.True(t, set.IsMember(hidden))
	assert.Equal(t, 2, set.MemberCount())
}

// An entry this hub can't open may be meant for another hub sharing the
// identity, or be junk. Either way it is skipped without error, and so is one
// that decrypts to anything but a pubkey.
func TestParseFollowSet_HiddenEntriesThisHubCannotReadAreSkipped(t *testing.T) {
	providerSK, _ := newKeypair(t)
	inboxSK, inboxPub := newKeypair(t)
	_, otherInboxPub := newKeypair(t)
	_, forOtherHub := newKeypair(t)
	_, mine := newKeypair(t)

	key, err := nip44.GenerateConversationKey(inboxPub, providerSK)
	require.NoError(t, err)
	notAPubkey, err := nip44.Encrypt("hello", key)
	require.NoError(t, err)
	upperCase, err := nip44.Encrypt("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", key)
	require.NoError(t, err)

	evt := signedFollowSetEvent(t, providerSK, testD, nil,
		hiddenEntry(t, providerSK, otherInboxPub, forOtherHub),
		nostr.Tag{"hidden_p", "not even base64"},
		nostr.Tag{"hidden_p", notAPubkey},
		nostr.Tag{"hidden_p", upperCase},
		hiddenEntry(t, providerSK, inboxPub, mine),
	)

	set := parseFollowSet(&evt, testD, inboxSK)
	assert.Equal(t, []string{mine}, set.Hidden)
	assert.False(t, set.IsMember(forOtherHub))
}

// §Hidden Members: the entry MUST NOT be encrypted to the hub connection key,
// whose secret every member holds. An entry encrypted that way does not open
// under the inbox key, so it never admits anyone.
func TestParseFollowSet_EntryEncryptedToConnectionKeyIsIgnored(t *testing.T) {
	providerSK, _ := newKeypair(t)
	inboxSK, _ := newKeypair(t)
	_, connectionPub := newKeypair(t)
	_, member := newKeypair(t)

	evt := signedFollowSetEvent(t, providerSK, testD, nil, hiddenEntry(t, providerSK, connectionPub, member))
	set := parseFollowSet(&evt, testD, inboxSK)
	assert.False(t, set.IsMember(member))
}

// Without an inbox key no hidden entry can be read, and the public members
// still count.
func TestParseFollowSet_NoInboxKey_PublicMembersOnly(t *testing.T) {
	providerSK, _ := newKeypair(t)
	_, inboxPub := newKeypair(t)
	_, hidden := newKeypair(t)

	evt := signedFollowSetEvent(t, providerSK, testD, []string{requesterPubkey}, hiddenEntry(t, providerSK, inboxPub, hidden))
	set := parseFollowSet(&evt, testD, "")
	assert.True(t, set.IsMember(requesterPubkey))
	assert.False(t, set.IsMember(hidden))
}

// NIP-51 private items in `content` are encrypted to the provider's own key.
// They MUST NOT be counted, even when they decrypt.
func TestParseFollowSet_PrivateItemsInContentAreNeverCounted(t *testing.T) {
	providerSK, provider := newKeypair(t)
	inboxSK, _ := newKeypair(t)
	_, private := newKeypair(t)

	selfKey, err := nip44.GenerateConversationKey(provider, providerSK)
	require.NoError(t, err)
	content, err := nip44.Encrypt(`[["p","`+private+`"]]`, selfKey)
	require.NoError(t, err)

	evt := nostr.Event{
		Kind:      db.KindFollowSet,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"d", testD}, {"p", requesterPubkey}},
		Content:   content,
	}
	require.NoError(t, evt.Sign(providerSK))

	set := parseFollowSet(&evt, testD, inboxSK)
	assert.False(t, set.IsMember(private))
	assert.Equal(t, 1, set.MemberCount())
}

// Only `d`, `p` and `hidden_p` decide membership. Other tags, a malformed `p`
// and a relay hint change nothing; title and description are display-only.
func TestParseFollowSet_NothingElseDecidesMembership(t *testing.T) {
	providerSK, _ := newKeypair(t)

	evt := signedFollowSetEvent(t, providerSK, testD, nil,
		nostr.Tag{"p", requesterPubkey, "wss://relay.example.com"},
		nostr.Tag{"p", "not-a-pubkey"},
		nostr.Tag{"P", unrelatedPubkey},
		nostr.Tag{"member", unrelatedPubkey},
		nostr.Tag{"max_amount", "999999999"},
		nostr.Tag{"title", "Family"},
	)
	set := parseFollowSet(&evt, testD, "")
	assert.Equal(t, []string{requesterPubkey}, set.Public)
	assert.Equal(t, "Family", set.Title)
}

// A pubkey listed both publicly and hidden is one member, reported as public.
// NIP-01 pubkeys are lowercase hex. A provider's uppercase `p` entry is
// dropped, not lowercased: it never admits anyone, which fails closed.
func TestParseFollowSet_UppercasePTagIsIgnored(t *testing.T) {
	providerSK, _ := newKeypair(t)
	upper := strings.ToUpper(requesterPubkey)
	ev := signedFollowSetEvent(t, providerSK, testD, []string{upper, unrelatedPubkey})

	set := parseFollowSet(&ev, testD, "")
	assert.False(t, set.IsMember(upper))
	assert.False(t, set.IsMember(requesterPubkey), "it is not matched case-insensitively either")
	assert.True(t, set.IsMember(unrelatedPubkey))
	assert.Equal(t, 1, set.MemberCount())
}

func TestParseFollowSet_PublicAndHiddenDuplicateCountsOnce(t *testing.T) {
	providerSK, _ := newKeypair(t)
	inboxSK, inboxPub := newKeypair(t)

	evt := signedFollowSetEvent(t, providerSK, testD, []string{requesterPubkey},
		hiddenEntry(t, providerSK, inboxPub, requesterPubkey))
	set := parseFollowSet(&evt, testD, inboxSK)
	assert.Equal(t, 1, set.MemberCount())
	assert.Empty(t, set.Hidden)
}

// --- Through IsAuthorized ---

// A resolved set with no `p` tags is empty: nobody is a member, and that is a
// definite answer, not NOT_READY. It is how a host closes a circle.
func TestIsAuthorized_FollowSet_FoundEmptySetAdmitsNobody(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()
	provider, evt := newSignedFollowSet(t, testD, nil, nostr.Tag{"title", "Closed"})
	useRelayServing(t, svc, evt)
	cache.ready.Store(true)

	authorized, err := cache.IsAuthorized(context.Background(), requesterPubkey, makeIdentity(db.CirclePolicyFollowSet, provider), svc.DB)
	require.NoError(t, err)
	assert.False(t, authorized)
}

// A hidden member is admitted end to end: fetched from a relay and decrypted
// with the hub's inbox key.
func TestIsAuthorized_FollowSet_HiddenMemberAdmitted(t *testing.T) {
	inboxSK, inboxPub := newKeypair(t)
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()
	cache.inboxKey = func() (string, error) { return inboxSK, nil }
	cache.ready.Store(true)

	providerSK, provider := newKeypair(t)
	_, hidden := newKeypair(t)
	evt := signedFollowSetEvent(t, providerSK, testD, []string{requesterPubkey}, hiddenEntry(t, providerSK, inboxPub, hidden))
	useRelayServing(t, svc, evt)

	identity := makeIdentity(db.CirclePolicyFollowSet, provider)
	for _, member := range []string{requesterPubkey, hidden} {
		authorized, err := cache.IsAuthorized(context.Background(), member, identity, svc.DB)
		require.NoError(t, err)
		assert.True(t, authorized, "member %s", member)
	}
	authorized, err := cache.IsAuthorized(context.Background(), unrelatedPubkey, identity, svc.DB)
	require.NoError(t, err)
	assert.False(t, authorized)
}

// NIP-CW §Membership: a Hub MUST NOT treat an unresolved set as an empty one.
// A hub whose relays never answer must say it isn't ready, which the caller
// can retry, never "not a member".
func TestFollowSetPolicy_UnresolvedSetIsNotADenial(t *testing.T) {
	origQ, origD := RelayQueryTimeout, outboxDiscoveryTimeout
	RelayQueryTimeout = 200 * time.Millisecond
	outboxDiscoveryTimeout = 200 * time.Millisecond
	t.Cleanup(func() { RelayQueryTimeout, outboxDiscoveryTimeout = origQ, origD })

	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	// Accepts the connection and the REQ, then never answers: no event, no EOSE.
	silent := newFakeRelay(t, func(*websocket.Conn, string, nostr.Filter) {})
	require.NoError(t, svc.Cfg.SetGeneralRelay(silent.URL))

	// What StartNostrSocialCacheRefresher does at startup: one sweep, then ready.
	ctx := context.Background()
	_ = cache.refreshOne(ctx, nostr.NewSimplePool(ctx), providerPubkey, testD)
	cache.ready.Store(true)

	identity := makeIdentity(db.CirclePolicyFollowSet, providerPubkey)
	authorized, err := cache.IsAuthorized(ctx, requesterPubkey, identity, svc.DB)
	assert.False(t, authorized)
	require.ErrorIs(t, err, constants.ErrCircleMembershipUnresolved)

	_, ok := cached(cache, providerPubkey, testD)
	assert.False(t, ok, "nothing is cached, so the next request fetches again")
}

// A relay still serving an older version must not roll back a newer cached
// one, such as the version this hub just published.
func TestStore_OlderVersionDoesNotReplaceNewer(t *testing.T) {
	providerSK, _ := newKeypair(t)
	cache := cacheWithInbox(t, "")

	newer := signedFollowSetEvent(t, providerSK, testD, []string{requesterPubkey})
	older := signedFollowSetEvent(t, providerSK, testD, []string{unrelatedPubkey})
	older.CreatedAt = newer.CreatedAt - 30
	require.NoError(t, older.Sign(providerSK))

	cache.storePublished(&newer, testD)
	kept := cache.store(parseFollowSet(&older, testD, ""))
	assert.Equal(t, newer.ID, kept.Event.ID)
	assert.True(t, kept.IsMember(requesterPubkey))
	assert.False(t, kept.IsMember(unrelatedPubkey))
}
