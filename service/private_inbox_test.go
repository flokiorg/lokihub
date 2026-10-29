package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/flokiorg/lokihub/tests"
)

// scriptedRelay is a fake relay a test can misbehave on demand: send CLOSED,
// drop the connection, or keep subscriptions open while delivering nothing. It
// applies `since` like a real relay, which is what made the inbox go deaf.
type scriptedRelay struct {
	url  string
	reqs chan nostr.Filter
	// published receives every event a client publishes, in order.
	published chan *nostr.Event

	mu    sync.Mutex
	conns map[*relayConn]bool
	deaf  bool
}

type relayConn struct {
	ws   *websocket.Conn
	wmu  sync.Mutex
	subs map[string]nostr.Filter
}

func (c *relayConn) send(msg ...any) {
	data, _ := json.Marshal(msg)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.WriteMessage(websocket.TextMessage, data)
}

func newScriptedRelay(t *testing.T) *scriptedRelay {
	t.Helper()
	r := &scriptedRelay{reqs: make(chan nostr.Filter, 64), published: make(chan *nostr.Event, 64), conns: map[*relayConn]bool{}}
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		c := &relayConn{ws: ws, subs: map[string]nostr.Filter{}}
		r.mu.Lock()
		r.conns[c] = true
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			delete(r.conns, c)
			r.mu.Unlock()
			_ = ws.Close()
		}()
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			r.handle(c, data)
		}
	}))
	t.Cleanup(srv.Close)
	r.url = strings.Replace(srv.URL, "http", "ws", 1)
	return r
}

func (r *scriptedRelay) handle(c *relayConn, data []byte) {
	var msg []json.RawMessage
	if json.Unmarshal(data, &msg) != nil || len(msg) < 2 {
		return
	}
	var label, id string
	_ = json.Unmarshal(msg[0], &label)
	switch label {
	case "REQ":
		_ = json.Unmarshal(msg[1], &id)
		var filter nostr.Filter
		if len(msg) > 2 {
			_ = json.Unmarshal(msg[2], &filter)
		}
		r.mu.Lock()
		c.subs[id] = filter
		r.mu.Unlock()
		c.send("EOSE", id)
		r.reqs <- filter
	case "CLOSE":
		_ = json.Unmarshal(msg[1], &id)
		r.mu.Lock()
		delete(c.subs, id)
		r.mu.Unlock()
	case "EVENT":
		var event nostr.Event
		if json.Unmarshal(msg[1], &event) != nil {
			return
		}
		c.send("OK", event.ID, true, "")
		select {
		case r.published <- &event:
		default:
		}
		r.deliver(&event)
	}
}

// deliver sends event to every matching subscription, unless the relay is deaf.
func (r *scriptedRelay) deliver(event *nostr.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deaf {
		return
	}
	for c := range r.conns {
		for id, filter := range c.subs {
			if filter.Matches(event) {
				go c.send("EVENT", id, event)
			}
		}
	}
}

func (r *scriptedRelay) closeSubscriptions() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for c := range r.conns {
		for id := range c.subs {
			go c.send("CLOSED", id, "error: shutting down")
			delete(c.subs, id)
		}
	}
}

func (r *scriptedRelay) dropConnections() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for c := range r.conns {
		_ = c.ws.Close()
	}
}

func (r *scriptedRelay) setDeaf(deaf bool) {
	r.mu.Lock()
	r.deaf = deaf
	r.mu.Unlock()
}

func (r *scriptedRelay) awaitREQ(t *testing.T) nostr.Filter {
	t.Helper()
	select {
	case filter := <-r.reqs:
		return filter
	case <-time.After(10 * time.Second):
		t.Fatal("no REQ reached the relay")
		return nostr.Filter{}
	}
}

// inboxEvent is shaped like a real wrapped envelope: addressed to inbox, long
// enough to pass the wire gate, and created up to two days in the past.
func inboxEvent(t *testing.T, inbox string) *nostr.Event {
	t.Helper()
	event := &nostr.Event{
		Kind:      transport.KindPrivateRequest,
		CreatedAt: nostr.Timestamp(time.Now().Add(-36 * time.Hour).Unix()),
		Tags:      nostr.Tags{{"p", inbox}},
		Content:   strings.Repeat("a", minWrapBytes),
	}
	require.NoError(t, event.Sign(nostr.GeneratePrivateKey()))
	return event
}

func awaitInboxEvent(t *testing.T, events <-chan nostr.RelayEvent, want *nostr.Event) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case got, ok := <-events:
			require.True(t, ok, "the inbox stream ended")
			if got.ID == want.ID {
				return
			}
		case <-deadline:
			t.Fatal("the inbox never delivered the event")
		}
	}
}

func inboxFilter(inbox string) nostr.Filter {
	return nostr.Filter{Kinds: []int{transport.KindPrivateRequest}, Tags: nostr.TagMap{"p": []string{inbox}}}
}

func TestSubscribeInbox_RelayClosesSubscription_ResubscribesAndKeepsDelivering(t *testing.T) {
	relay := newScriptedRelay(t)
	inbox := tests.RandomHex32()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := subscribeInbox(ctx, nostr.NewSimplePool(ctx), []string{relay.url}, inboxFilter(inbox))
	relay.awaitREQ(t)
	first := inboxEvent(t, inbox)
	relay.deliver(first)
	awaitInboxEvent(t, events, first)

	relay.closeSubscriptions()
	again := relay.awaitREQ(t)
	require.Nil(t, again.Since, "the resubscription must not narrow the filter with since")

	second := inboxEvent(t, inbox)
	relay.deliver(second)
	awaitInboxEvent(t, events, second)
}

func TestSubscribeInbox_ConnectionDrops_ResubscribesAndKeepsDelivering(t *testing.T) {
	// go-nostr v0.52.3 races on its own Relay.Connection field whenever the
	// server drops a connection (the write loop nils it while close() reads it),
	// so a -race build flags that intermittently, here or in production code.
	if raceDetectorEnabled {
		t.Skip("a server-side drop trips go-nostr's own data race on Relay.Connection; asserted in the ordinary (non -race) run")
	}
	relay := newScriptedRelay(t)
	inbox := tests.RandomHex32()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := subscribeInbox(ctx, nostr.NewSimplePool(ctx), []string{relay.url}, inboxFilter(inbox))
	relay.awaitREQ(t)

	relay.dropConnections()
	again := relay.awaitREQ(t)
	require.Nil(t, again.Since, "the resubscription must not narrow the filter with since")

	// Backdated like every real envelope: a `since` set at reconnect time would
	// make the relay withhold it.
	event := inboxEvent(t, inbox)
	relay.deliver(event)
	awaitInboxEvent(t, events, event)
}

// The relay keeps the subscription open but stops delivering, with no CLOSED and
// no dropped connection — what the lab hub saw. Only the liveness probe notices.
func TestPrivateTransportSubscription_SilentRelay_ProbeResubscribesAndDeliveryResumes(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	relay := newScriptedRelay(t)
	require.NoError(t, ts.Cfg.SetUpdate("Relay", relay.url, ""))

	svc := &service{cfg: ts.Cfg, keys: ts.Keys}
	inbox := tests.RandomHex32()
	pt := &privateTransport{
		inboxXOnly:    inbox,
		nonces:        newMemServedNonces(),
		probeInterval: 200 * time.Millisecond,
		probeTimeout:  300 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		return svc.startPrivateTransportSubscription(groupCtx, nostr.NewSimplePool(groupCtx), pt, group)
	})
	t.Cleanup(func() {
		cancel()
		_ = group.Wait()
	})

	relay.awaitREQ(t)
	relay.setDeaf(true)
	require.Eventually(t, func() bool {
		return svc.GetPrivateTransportStatus().InboxResubscribes > 0
	}, 10*time.Second, 20*time.Millisecond, "a silent inbox must be resubscribed")
	require.False(t, svc.GetPrivateTransportStatus().InboxDelivering)

	relay.setDeaf(false)
	require.Eventually(t, func() bool {
		return svc.GetPrivateTransportStatus().InboxDelivering
	}, 15*time.Second, 20*time.Millisecond, "the probe comes back once the relay delivers again")

	// A real event reaches the transport again: it passes the wire gate and is
	// counted when it fails to unwrap.
	before := pt.rejectedEnvelopes.Load()
	relay.deliver(inboxEvent(t, inbox))
	require.Eventually(t, func() bool {
		return pt.rejectedEnvelopes.Load() > before
	}, 10*time.Second, 20*time.Millisecond, "delivery continues after the resubscribe")
}
