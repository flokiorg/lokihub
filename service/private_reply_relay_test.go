package service

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/flokiorg/lokihub/nip47"
	"github.com/flokiorg/lokihub/tests"
)

// A private reply goes to the relay its request arrived on: that is where the client
// sent the request and where it subscribes for the answer. The hub used to publish to
// the first of its configured relays that accepted, so a request sent to any other of
// its relays was answered somewhere the client wasn't listening.

func requireReplyOn(t *testing.T, relay *scriptedRelay, replyTo string) {
	t.Helper()
	select {
	case reply := <-relay.published:
		require.Equal(t, transport.KindPrivateResponse, reply.Kind)
		require.Equal(t, replyTo, firstPTagValue(reply))
	case <-time.After(10 * time.Second):
		t.Fatalf("no reply was published on %s", relay.url)
	}
}

func requireNothingOn(t *testing.T, relay *scriptedRelay) {
	t.Helper()
	select {
	case <-relay.published:
		t.Fatalf("a reply was published on %s, which the request did not arrive on", relay.url)
	case <-time.After(300 * time.Millisecond):
	}
}

func twoRelayService(t *testing.T) (*tests.TestService, *service, *scriptedRelay, *scriptedRelay) {
	t.Helper()
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(ts.Remove)
	first, second := newScriptedRelay(t), newScriptedRelay(t)
	require.NoError(t, ts.Cfg.SetUpdate("Relay", first.url+","+second.url, ""))
	return ts, &service{cfg: ts.Cfg, keys: ts.Keys}, first, second
}

func TestPublishPrivateReply_GoesToTheRelayTheRequestArrivedOn(t *testing.T) {
	_, svc, first, second := twoRelayService(t)

	var conversationKey [32]byte
	_, err := rand.Read(conversationKey[:])
	require.NoError(t, err)
	envelope := &transport.Envelope{Nonce: tests.RandomHex32(), ReplyTo: tests.RandomHex32()}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.publishPrivateReply(ctx, nostr.NewSimplePool(ctx), envelope, conversationKey, second.url, nil))

	requireReplyOn(t, second, envelope.ReplyTo)
	requireNothingOn(t, first)
}

// If the arrival relay refuses, the reply still goes out on another configured relay:
// the items have already run.
func TestPublishPrivateReply_ArrivalRelayUnreachable_FallsBackToTheOthers(t *testing.T) {
	_, svc, first, _ := twoRelayService(t)

	var conversationKey [32]byte
	envelope := &transport.Envelope{Nonce: tests.RandomHex32(), ReplyTo: tests.RandomHex32()}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.publishPrivateReply(ctx, nostr.NewSimplePool(ctx), envelope, conversationKey, "ws://127.0.0.1:1", nil))

	requireReplyOn(t, first, envelope.ReplyTo)
}

// End to end through the subscription: a request delivered by the second of the hub's
// two relays is answered there. The same path serves NIP-CW private joins
// (create_circle_wallet is a privately servable method).
func TestPrivateSubscription_ReplyGoesToTheRelayTheRequestArrivedOn(t *testing.T) {
	ts, svc, first, second := twoRelayService(t)
	svc.nip47Service = nip47.NewNip47Service(ts.DB, ts.Cfg, ts.Keys, ts.EventPublisher, nil)
	svc.lnClient = ts.LNClient
	pt, seal := newUnwrapFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := nostr.NewSimplePool(ctx)
	arrivedOn, err := pool.EnsureRelay(second.url)
	require.NoError(t, err)

	env := testEnvelope(t) // its one item is omitted; an empty reply is still a reply
	events := make(chan nostr.RelayEvent, 1)
	events <- nostr.RelayEvent{Event: seal(t, env), Relay: arrivedOn}
	close(events)

	group, groupCtx := errgroup.WithContext(ctx)
	_ = svc.watchPrivateSubscription(groupCtx, pool, events, pt, group)

	requireReplyOn(t, second, env.ReplyTo)
	requireNothingOn(t, first)
	cancel()
	_ = group.Wait()
}
