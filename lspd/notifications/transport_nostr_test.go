package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingRelayServer is a minimal in-memory Nostr relay that just accepts
// WebSocket connections and counts how many real connection attempts it
// received — enough to prove the F-6 fix without a full protocol
// implementation (RelayPool.GetOrConnect only needs the handshake to
// succeed to consider a relay connected).
type countingRelayServer struct {
	addr        string
	connections int32
	server      *http.Server
}

func newCountingRelayServer(t *testing.T) *countingRelayServer {
	t.Helper()
	s := &countingRelayServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	s.server = &http.Server{Handler: mux}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s.addr = ln.Addr().String()

	go func() { _ = s.server.Serve(ln) }()
	t.Cleanup(func() { _ = s.server.Close() })
	return s
}

func (s *countingRelayServer) handle(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&s.connections, 1)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	ctx := r.Context()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
	}
}

func (s *countingRelayServer) url() string {
	return "ws://" + s.addr
}

func (s *countingRelayServer) connectionCount() int32 {
	return atomic.LoadInt32(&s.connections)
}

// TestRelayPool_ConcurrentGetOrConnect_SameURL is the regression test for
// F-6: GetOrConnect's load-check-connect-store had no per-URL lock, so N
// concurrent notifications to the same relay could each miss the cache,
// each dial, and each Store — all but the last connection leaked, never
// closed. With the singleflight fix, N concurrent calls for the same URL
// must result in exactly one real connection.
func TestRelayPool_ConcurrentGetOrConnect_SameURL(t *testing.T) {
	server := newCountingRelayServer(t)
	pool := NewRelayPool()

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := pool.GetOrConnect(ctx, server.url())
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		assert.NoError(t, err)
	}

	// Give the server a moment in case any stray connection attempt is
	// still in flight (shouldn't be, but avoids a flaky read of the
	// counter mid-accept).
	time.Sleep(50 * time.Millisecond)
	assert.EqualValues(t, 1, server.connectionCount(), "20 concurrent GetOrConnect calls for the same URL must result in exactly one real connection")
}

// TestRelayPool_ConcurrentGetOrConnect_DifferentURLs confirms the
// singleflight fix doesn't over-serialize: different URLs must still
// connect independently and in parallel, not queue behind each other.
func TestRelayPool_ConcurrentGetOrConnect_DifferentURLs(t *testing.T) {
	serverA := newCountingRelayServer(t)
	serverB := newCountingRelayServer(t)
	pool := NewRelayPool()

	var wg sync.WaitGroup
	wg.Add(2)
	var errA, errB error
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, errA = pool.GetOrConnect(ctx, serverA.url())
	}()
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, errB = pool.GetOrConnect(ctx, serverB.url())
	}()
	wg.Wait()

	assert.NoError(t, errA)
	assert.NoError(t, errB)
	time.Sleep(50 * time.Millisecond)
	assert.EqualValues(t, 1, serverA.connectionCount())
	assert.EqualValues(t, 1, serverB.connectionCount())
}

// ackingRelayServer is a minimal in-memory Nostr relay that actually speaks
// enough of the protocol to complete a publish: it parses each incoming
// ["EVENT", {...}] message and immediately replies ["OK", id, ok, reason],
// letting Relay.Publish (and therefore NostrTransport.Send) succeed or fail
// deterministically without a real relay.
type ackingRelayServer struct {
	addr   string
	ok     bool
	reason string
	server *http.Server
}

func newAckingRelayServer(t *testing.T, ok bool, reason string) *ackingRelayServer {
	t.Helper()
	s := &ackingRelayServer{ok: ok, reason: reason}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	s.server = &http.Server{Handler: mux}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s.addr = ln.Addr().String()

	go func() { _ = s.server.Serve(ln) }()
	t.Cleanup(func() { _ = s.server.Close() })
	return s
}

func (s *ackingRelayServer) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	ctx := r.Context()
	parser := nostr.NewMessageParser()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		env, err := parser.ParseMessage(string(data))
		if err != nil {
			continue
		}
		evEnv, isEvent := env.(*nostr.EventEnvelope)
		if !isEvent {
			continue
		}
		reply, _ := json.Marshal([]interface{}{"OK", evEnv.ID, s.ok, s.reason})
		if err := conn.Write(ctx, websocket.MessageText, reply); err != nil {
			return
		}
	}
}

func (s *ackingRelayServer) url() string {
	return "ws://" + s.addr
}

// nostrLsps5Uri builds a nostr+lsps5:// URI for the given (already-hex,
// 64-char) recipient pubkey and relay URLs, matching what
// nostrlsps5.ParseLsp5Uri expects.
func nostrLsps5Uri(pubkey string, relays ...string) string {
	q := make([]string, len(relays))
	for i, r := range relays {
		q[i] = "relay=" + r
	}
	uri := fmt.Sprintf("nostr+lsps5://%s", pubkey)
	if len(q) > 0 {
		uri += "?" + strings.Join(q, "&")
	}
	return uri
}

func TestNostrTransport_Type(t *testing.T) {
	transport := NewNostrTransport(nostr.GeneratePrivateKey())
	assert.Equal(t, TransportTypeNostr, transport.Type())
}

func TestNostrTransport_Send_MissingPrivKey(t *testing.T) {
	transport := NewNostrTransport("")
	err := transport.Send(context.Background(), nostrLsps5Uri(nostr.GeneratePrivateKey()), "lsps5.payment_incoming", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestNostrTransport_Send_InvalidUri(t *testing.T) {
	transport := NewNostrTransport(nostr.GeneratePrivateKey())
	err := transport.Send(context.Background(), "https://not-a-nostr-uri.example", "lsps5.payment_incoming", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid nostr URI")
}

func TestNostrTransport_Send_NoRelaysInUri(t *testing.T) {
	transport := NewNostrTransport(nostr.GeneratePrivateKey())
	recipientPriv := nostr.GeneratePrivateKey()
	recipientPub, err := nostr.GetPublicKey(recipientPriv)
	require.NoError(t, err)

	err = transport.Send(context.Background(), nostrLsps5Uri(recipientPub), "lsps5.payment_incoming", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no relays specified")
}

func TestNostrTransport_Send_EventCreationFailure(t *testing.T) {
	transport := NewNostrTransport(nostr.GeneratePrivateKey())
	// 64 chars but not valid hex, so ComputeSharedSecret's hex.DecodeString
	// fails inside CreateNotificationEvent, before any network I/O.
	invalidPubkey := strings.Repeat("z", 64)

	err := transport.Send(context.Background(), nostrLsps5Uri(invalidPubkey, "wss://relay.example"), "lsps5.payment_incoming", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create notification event")
}

func TestNostrTransport_Send_SucceedsWhenRelayAcks(t *testing.T) {
	relay := newAckingRelayServer(t, true, "")
	transport := NewNostrTransport(nostr.GeneratePrivateKey())
	recipientPriv := nostr.GeneratePrivateKey()
	recipientPub, err := nostr.GetPublicKey(recipientPriv)
	require.NoError(t, err)

	err = transport.Send(context.Background(), nostrLsps5Uri(recipientPub, relay.url()), "lsps5.payment_incoming", map[string]string{"payment_hash": "abc"})
	assert.NoError(t, err)
}

func TestNostrTransport_Send_FailsWhenAllRelaysNack(t *testing.T) {
	relayA := newAckingRelayServer(t, false, "blocked: rate-limited")
	relayB := newAckingRelayServer(t, false, "blocked: rate-limited")
	transport := NewNostrTransport(nostr.GeneratePrivateKey())
	recipientPriv := nostr.GeneratePrivateKey()
	recipientPub, err := nostr.GetPublicKey(recipientPriv)
	require.NoError(t, err)

	err = transport.Send(context.Background(), nostrLsps5Uri(recipientPub, relayA.url(), relayB.url()), "lsps5.payment_incoming", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to publish to any relay")
}
