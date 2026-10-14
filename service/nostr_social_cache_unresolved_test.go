package service

import (
	"context"
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

// useAnsweringRelay points the General relays at one that answers every query with
// EOSE and no event, so a fetch resolves (to an empty list) instead of timing out.
func useAnsweringRelay(t *testing.T, svc *tests.TestService) {
	t.Helper()
	relay := newFakeRelay(t, func(conn *websocket.Conn, subID string, _ nostr.Filter) {
		sendEOSE(t, conn, subID)
	})
	require.NoError(t, svc.Cfg.SetGeneralRelay(relay.URL))
}

// NIP-CW §Membership: a Hub MUST NOT treat an unresolved list as an empty one. A
// following-policy Hub whose relays never answer must say it isn't ready, which
// the caller can retry, never "not a member".
func TestFollowingPolicy_UnresolvedListIsNotADenial(t *testing.T) {
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
	_ = cache.refreshOne(ctx, nostr.NewSimplePool(ctx), providerPubkey)
	cache.ready.Store(true)

	identity := makeIdentity(db.CirclePolicyFollowing, providerPubkey)
	authorized, err := cache.IsAuthorized(ctx, requesterPubkey, identity, svc.DB)
	assert.False(t, authorized)
	require.ErrorIs(t, err, constants.ErrCircleMembershipUnresolved)

	_, cached := cache.peek(providerPubkey)
	assert.False(t, cached, "nothing is cached, so the next request fetches again")
}
