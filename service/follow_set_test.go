package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	nmilatnip01 "github.com/ohstr/nmilat/nip01"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// storingRelay is a fake relay that keeps the latest version of each
// addressable event it is sent and serves it back to matching REQs, so a
// publish and the read that follows go through a real round trip.
type storingRelay struct {
	*httptest.Server
	mu     sync.Mutex
	events map[string]nostr.Event // keyed by kind:pubkey:d
}

func (r *storingRelay) put(ev nostr.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := followSetKey(ev.PubKey, ev.Tags.GetD())
	if existing, ok := r.events[key]; ok && supersedes(&existing, &ev) {
		return
	}
	r.events[key] = ev
}

func (r *storingRelay) get(provider, d string) (nostr.Event, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev, ok := r.events[followSetKey(provider, d)]
	return ev, ok
}

func newStoringRelay(t *testing.T) *storingRelay {
	t.Helper()
	relay := &storingRelay{events: map[string]nostr.Event{}}
	parser := nostr.NewMessageParser()
	relay.Server = httptest.NewServer(&websocket.Server{
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
				switch env := env.(type) {
				case *nostr.EventEnvelope:
					relay.put(env.Event)
					ok := nostr.OKEnvelope{EventID: env.Event.ID, OK: true}
					raw, _ := ok.MarshalJSON()
					_ = websocket.Message.Send(conn, string(raw))
				case *nostr.ReqEnvelope:
					var matching []nostr.Event
					relay.mu.Lock()
					for _, ev := range relay.events {
						for _, filter := range env.Filters {
							if filter.Matches(&ev) {
								matching = append(matching, ev)
								break
							}
						}
					}
					relay.mu.Unlock()
					sendEvents(t, conn, env.SubscriptionID, matching...)
				}
			}
		},
	})
	t.Cleanup(relay.Close)
	return relay
}

// newFollowSetPublisher wires a service whose node can sign and whose private
// transport is running, publishing to relay.
func newFollowSetPublisher(t *testing.T, relay *storingRelay) (*service, string) {
	t.Helper()
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(ts.Remove)
	require.NoError(t, ts.Cfg.SetGeneralRelay(relay.URL))

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)

	cache := NewNostrSocialCache(ts.Cfg)
	cache.inboxKey = func() (string, error) { return ts.Keys.GetPrivateTransportKey(privateTransportKeyIndex) }
	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln, socialCache: cache}

	node, err := svc.NodeNostrIdentity()
	require.NoError(t, err)
	inboxSK, err := ts.Keys.GetPrivateTransportKey(privateTransportKeyIndex)
	require.NoError(t, err)
	inboxPub, err := nostr.GetPublicKey(inboxSK)
	require.NoError(t, err)
	svc.setPrivateTransportIdentity(node, inboxPub)
	return svc, node
}

func randomMembers(t *testing.T, n int) []string {
	t.Helper()
	members := make([]string, n)
	for i := range members {
		_, members[i] = newKeypair(t)
	}
	return members
}

func hiddenPayloads(ev nostr.Event) []string {
	var payloads []string
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "hidden_p" {
			payloads = append(payloads, tag[1])
		}
	}
	return payloads
}

func TestPublishFollowSet_SignedByNodeWithPublicAndHiddenMembers(t *testing.T) {
	relay := newStoringRelay(t)
	svc, node := newFollowSetPublisher(t, relay)
	members := randomMembers(t, 2)
	public, hidden := members[0], members[1]

	set, err := svc.PublishFollowSet(context.Background(), testD, FollowSetContent{
		Title:       "Ada's Family Circle",
		Description: "Wallets for the family",
		Public:      []string{public},
		Hidden:      []string{hidden},
	})
	require.NoError(t, err)
	assert.True(t, set.IsMember(public))
	assert.True(t, set.IsMember(hidden))

	ev, ok := relay.get(node, testD)
	require.True(t, ok, "the set reached the relay")
	assert.Equal(t, db.KindFollowSet, ev.Kind)
	assert.Equal(t, node, ev.PubKey)
	valid, err := ev.CheckSignature()
	require.NoError(t, err)
	assert.True(t, valid)
	assert.Equal(t, "Ada's Family Circle", ev.Tags.GetFirst([]string{"title"}).Value())
	assert.Equal(t, "Wallets for the family", ev.Tags.GetFirst([]string{"description"}).Value())

	// The hidden member is never in a `p` tag, and its entry opens under the
	// node/inbox conversation key only.
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "p" {
			assert.NotEqual(t, hidden, tag[1])
		}
	}
	payloads := hiddenPayloads(ev)
	require.Len(t, payloads, 1)
	inboxSK, err := svc.keys.GetPrivateTransportKey(privateTransportKeyIndex)
	require.NoError(t, err)
	key, err := nip44.GenerateConversationKey(node, inboxSK)
	require.NoError(t, err)
	plaintext, err := nip44.Decrypt(payloads[0], key)
	require.NoError(t, err)
	assert.Equal(t, hidden, plaintext)

	// A fresh reader with the same inbox key resolves both members from the relay.
	reader := NewNostrSocialCache(svc.cfg)
	reader.inboxKey = svc.socialCache.inboxKey
	read, err := reader.RefreshFollowSet(context.Background(), node, testD)
	require.NoError(t, err)
	assert.Equal(t, []string{public}, read.Public)
	assert.Equal(t, []string{hidden}, read.Hidden)
}

// §Hidden Members: every publish encrypts the entries afresh and shuffles
// them, so two versions don't show which entry changed.
func TestPublishFollowSet_ReencryptsAndShufflesHiddenEntries(t *testing.T) {
	relay := newStoringRelay(t)
	svc, node := newFollowSetPublisher(t, relay)
	hidden := randomMembers(t, 6)
	sorted := append([]string{}, hidden...)
	sort.Strings(sorted)

	var previous []string
	orders := map[string]struct{}{}
	for i := 0; i < 5; i++ {
		set, err := svc.PublishFollowSet(context.Background(), testD, FollowSetContent{Hidden: sorted})
		require.NoError(t, err)
		payloads := hiddenPayloads(*set.Event)
		require.Len(t, payloads, len(hidden))
		for _, p := range payloads {
			assert.NotContains(t, previous, p, "no payload is reused across publishes")
		}
		previous = payloads

		order := ""
		for _, member := range set.Hidden {
			order += member
		}
		orders[order] = struct{}{}
	}
	assert.Greater(t, len(orders), 1, "the entry order changes between publishes")

	ev, _ := relay.get(node, testD)
	assert.Len(t, hiddenPayloads(ev), len(hidden))
}

// A republish keeps the tags and content lokihub doesn't edit, and supersedes
// the current version even within the same second.
func TestPublishFollowSet_PreservesUnknownTagsAndSupersedes(t *testing.T) {
	relay := newStoringRelay(t)
	svc, node := newFollowSetPublisher(t, relay)
	members := randomMembers(t, 2)

	first, err := svc.PublishFollowSet(context.Background(), testD, FollowSetContent{Title: "Family", Public: []string{members[0]}})
	require.NoError(t, err)

	// A version with an image and a private-items content blob, as another
	// NIP-51 client would leave it.
	var tags [][]string
	for _, tag := range first.Event.Tags {
		tags = append(tags, tag)
	}
	tags = append(tags, []string{"image", "https://example.com/a.png"}, []string{"x-custom", "keep me"})
	unsigned := nmilatnip01.NewUnsignedEvent(db.KindFollowSet, node, "opaque-private-items", tags...)
	unsigned.CreatedAt = uint64(first.Event.CreatedAt) + 1
	edited, err := svc.signNodeEvent(context.Background(), unsigned)
	require.NoError(t, err)
	relay.put(*edited)

	second, err := svc.PublishFollowSet(context.Background(), testD, FollowSetContent{Title: "Family v2", Public: []string{members[1]}})
	require.NoError(t, err)

	ev, ok := relay.get(node, testD)
	require.True(t, ok)
	assert.Equal(t, second.Event.ID, ev.ID)
	assert.Greater(t, ev.CreatedAt, edited.CreatedAt, "the new version must supersede the one it edits")
	assert.Equal(t, "https://example.com/a.png", ev.Tags.GetFirst([]string{"image"}).Value())
	assert.Equal(t, "keep me", ev.Tags.GetFirst([]string{"x-custom"}).Value())
	assert.Equal(t, "opaque-private-items", ev.Content)
	assert.Equal(t, "Family v2", ev.Tags.GetFirst([]string{"title"}).Value())
	assert.Equal(t, []string{members[1]}, second.Public, "members are replaced, not merged")
}

// Closing a circle publishes the set with no members; it is then resolved and
// empty, not unresolved.
func TestPublishFollowSet_EmptySetClosesTheCircle(t *testing.T) {
	relay := newStoringRelay(t)
	svc, node := newFollowSetPublisher(t, relay)
	members := randomMembers(t, 1)

	_, err := svc.PublishFollowSet(context.Background(), testD, FollowSetContent{Public: members})
	require.NoError(t, err)
	_, err = svc.PublishFollowSet(context.Background(), testD, FollowSetContent{})
	require.NoError(t, err)

	authorized, err := svc.socialCache.IsAuthorized(context.Background(), members[0],
		&db.CircleIdentity{Policy: db.CirclePolicyFollowSet, ProviderPubkey: node, FollowSetD: testD}, nil)
	require.NoError(t, err, "a closed circle is a definite answer, not NOT_READY")
	assert.False(t, authorized)
}

// §Hidden Members: a hub that publishes no announcement can't have hidden
// members, since no provider could know its inbox key.
func TestPublishFollowSet_HiddenMembersNeedThePrivateTransport(t *testing.T) {
	relay := newStoringRelay(t)
	svc, node := newFollowSetPublisher(t, relay)
	svc.resetPrivateTransportStatus()

	_, err := svc.PublishFollowSet(context.Background(), testD, FollowSetContent{Hidden: randomMembers(t, 1)})
	require.ErrorIs(t, err, constants.ErrInvalidParams)
	_, published := relay.get(node, testD)
	assert.False(t, published)

	// Public members alone still publish.
	_, err = svc.PublishFollowSet(context.Background(), testD, FollowSetContent{Public: randomMembers(t, 1)})
	require.NoError(t, err)
}

func TestPublishFollowSet_InvalidContentRejected(t *testing.T) {
	relay := newStoringRelay(t)
	svc, node := newFollowSetPublisher(t, relay)
	members := randomMembers(t, 1)

	for name, tc := range map[string]struct {
		d       string
		content FollowSetContent
	}{
		"empty d":              {"", FollowSetContent{}},
		"malformed member":     {testD, FollowSetContent{Public: []string{"npub1notahexkey"}}},
		"uppercase member":     {testD, FollowSetContent{Public: []string{"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}},
		"public and hidden":    {testD, FollowSetContent{Public: members, Hidden: members}},
		"listed twice":         {testD, FollowSetContent{Public: []string{members[0], members[0]}}},
		"title too long":       {testD, FollowSetContent{Title: string(make([]byte, 256))}},
		"description too long": {testD, FollowSetContent{Description: string(make([]byte, 1001))}},
	} {
		_, err := svc.PublishFollowSet(context.Background(), tc.d, tc.content)
		require.ErrorIs(t, err, constants.ErrInvalidParams, name)
	}
	_, published := relay.get(node, testD)
	assert.False(t, published, "nothing invalid reaches a relay")
}
