package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Addressing the Response and §The Reply Key: a kind-23191 reply is
// p-tagged with the request's reply_to, and encrypted under the key derived
// from the request's conversation key and that reply_to. Either one wrong makes
// every reply unreadable after the request's money has already moved, so the
// reply is opened here exactly the way a client opens it.
func TestPublishPrivateReply_ClientFindsAndOpensIt(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	relay := newScriptedRelay(t)
	require.NoError(t, ts.Cfg.SetUpdate("Relay", relay.url, ""))
	svc := &service{cfg: ts.Cfg, keys: ts.Keys}

	var conversationKey [32]byte
	_, err = rand.Read(conversationKey[:])
	require.NoError(t, err)
	envelope := &transport.Envelope{Nonce: tests.RandomHex32(), ReplyTo: tests.RandomHex32()}
	results := []transport.Result{{ID: "item-1", ResultType: "cash_status", Result: json.RawMessage(`{"ok":true}`)}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.publishPrivateReply(ctx, nostr.NewSimplePool(ctx), envelope, conversationKey, relay.url, results))

	var reply *nostr.Event
	select {
	case reply = <-relay.published:
	case <-time.After(5 * time.Second):
		t.Fatal("no reply was published")
	}

	// Found the way a client subscribes: {"kinds":[23191],"#p":["<reply_to>"]}.
	filter := nostr.Filter{Kinds: []int{transport.KindPrivateResponse}, Tags: nostr.TagMap{"p": []string{envelope.ReplyTo}}}
	assert.True(t, filter.Matches(reply), "the reply must be p-tagged with reply_to, never the nonce")

	// Opened the way a client opens it: under the derived reply key.
	replyKey, err := transport.DeriveReplyKey(conversationKey, envelope.ReplyTo)
	require.NoError(t, err)
	plaintext, err := nip44.Decrypt(reply.Content, replyKey)
	require.NoError(t, err, "the reply must be encrypted under the derived reply key")
	decoded, err := transport.DecodeResponse([]byte(plaintext), envelope.Nonce, []string{"item-1"}, ts.Cfg.PrivateEnvelopeLimits())
	require.NoError(t, err)
	require.Len(t, decoded.Results, 1)
	assert.JSONEq(t, `{"ok":true}`, string(decoded.Results[0].Result))

	_, err = nip44.Decrypt(reply.Content, conversationKey)
	assert.Error(t, err, "the bare conversation key must not open the reply")
}

// NIP-CASH §The Reply Key (CASH-285): each kind-23191 reply is signed with a
// fresh key used once, never the node key.
func TestPublishPrivateReply_EachReplyIsSignedByAFreshKey(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	relay := newScriptedRelay(t)
	require.NoError(t, ts.Cfg.SetUpdate("Relay", relay.url, ""))
	svc := &service{cfg: ts.Cfg, keys: ts.Keys}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := nostr.NewSimplePool(ctx)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		var ck [32]byte
		_, _ = rand.Read(ck[:])
		env := &transport.Envelope{Nonce: tests.RandomHex32(), ReplyTo: tests.RandomHex32()}
		require.NoError(t, svc.publishPrivateReply(ctx, pool, env, ck, relay.url, nil))
		select {
		case ev := <-relay.published:
			require.False(t, seen[ev.PubKey], "a reply author key was used twice")
			require.NotEqual(t, ts.Keys.GetNostrPublicKey(), ev.PubKey)
			seen[ev.PubKey] = true
		case <-time.After(5 * time.Second):
			t.Fatal("no reply was published")
		}
	}
}
