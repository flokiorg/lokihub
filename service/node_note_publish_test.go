package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests"
)

// This file covers PublishNodeNote, reusing the announcingLN/newAnnouncingLN
// fixtures private_announcement_publish_test.go already built — same
// LN-backend and relay failure modes as PublishNodeProfile apply here, since
// both go through the shared publishNodeEvent helper. What's specific to
// this file is TestPublishNodeNote_SignsWithNodeIdentityNotInboxKey: the
// user explicitly asked to confirm the signed event's pubkey is the LN
// node's own identity, never the private-transport inbox key, so this pins
// that with an assertion against the actual bytes a relay receives — not
// just a code read.

func TestPublishNodeNote_RequiresLNClient(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	svc := &service{cfg: ts.Cfg, keys: ts.Keys}
	_, err = svc.PublishNodeNote(context.Background(), "hello")
	require.ErrorContains(t, err, "publishing a node event needs an LN client")
}

func TestPublishNodeNote_RejectsEmptyContent(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	svc := &service{cfg: ts.Cfg, keys: ts.Keys}
	_, err = svc.PublishNodeNote(context.Background(), "   ")
	require.ErrorContains(t, err, "note content cannot be empty")
}

func TestPublishNodeNote_RequiresTransportSigner(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	base.Pubkey = hex.EncodeToString(priv.PubKey().SerializeCompressed())

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ts.LNClient}
	_, err = svc.PublishNodeNote(context.Background(), "hello")
	require.ErrorContains(t, err, "this LN backend cannot sign a node event")
}

func TestPublishNodeNote_PropagatesSignerError(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)
	ln.signErr = errors.New("node locked")

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	_, err = svc.PublishNodeNote(context.Background(), "hello")
	require.ErrorContains(t, err, "node refused to sign the event")
	require.ErrorContains(t, err, "node locked")
}

func TestPublishNodeNote_NoGeneralRelaysConfiguredIsAnError(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	require.NoError(t, ts.Cfg.SetUpdate("GeneralRelay", "", ""))

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	_, err = svc.PublishNodeNote(context.Background(), "hello")
	require.ErrorContains(t, err, "no general relays configured")
}

func TestPublishNodeNote_NoRelayAcceptedIsAnError(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	require.NoError(t, ts.Cfg.SetUpdate("GeneralRelay", "ws://127.0.0.1:1", ""))

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	_, err = svc.PublishNodeNote(context.Background(), "hello")
	require.ErrorContains(t, err, "no relay accepted the node event")
}

func TestPublishNodeNote_SucceedsWhenAtLeastOneRelayAccepts(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	good := newOKRelay(t)
	require.NoError(t, ts.Cfg.SetUpdate("GeneralRelay", "ws://127.0.0.1:1,"+good, ""))

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	published, err := svc.PublishNodeNote(context.Background(), "hello from the hub")
	require.NoError(t, err, "one reachable relay accepting the event must be enough")
	require.NotEmpty(t, published.ID)
}

// TestPublishNodeNote_SignsWithNodeIdentityNotInboxKey captures the actual
// event bytes a relay receives and checks its pubkey field against
// nodeXOnlyIdentity() (derived from the LN node's own compressed pubkey) —
// the concrete check behind "make sure we can sign w[ith] [the] ln identity
// nostr identity not the inbox one". A privateTransport's inbox key is a
// separate, ephemeral key (see keys.GetPrivateTransportKey); this test
// doesn't even construct one, so the only identity in play is the node's.
func TestPublishNodeNote_SignsWithNodeIdentityNotInboxKey(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	capture := newCapturingRelay(t)
	require.NoError(t, ts.Cfg.SetUpdate("GeneralRelay", capture.url, ""))

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	_, err = svc.PublishNodeNote(context.Background(), "hello from the hub")
	require.NoError(t, err)

	wantNodeXOnly, err := svc.nodeXOnlyIdentity()
	require.NoError(t, err)
	require.Equal(t, ln.pub[2:], wantNodeXOnly, "sanity: nodeXOnlyIdentity must match the LN backend's own compressed pubkey minus its prefix byte")

	events := capture.received()
	require.Len(t, events, 1)
	require.Equal(t, wantNodeXOnly, events[0].Pubkey, "the published note must be signed by the node identity, not the (unrelated, here unconstructed) inbox key")
	require.Equal(t, "hello from the hub", events[0].Content)
}

type capturedEvent struct {
	Kind    int    `json:"kind"`
	Pubkey  string `json:"pubkey"`
	Content string `json:"content"`
}

type capturingRelay struct {
	url string
	mu  sync.Mutex
	evs []capturedEvent
}

func (r *capturingRelay) received() []capturedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]capturedEvent, len(r.evs))
	copy(out, r.evs)
	return out
}

// newCapturingRelay is newOKRelay's sibling: same ack-everything behavior,
// but it also records each event's pubkey/content so a test can assert on
// what was actually signed and sent, not just that publishing succeeded.
func newCapturingRelay(t *testing.T) *capturingRelay {
	t.Helper()
	r := &capturingRelay{}
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var arr []json.RawMessage
			if jsonErr := json.Unmarshal(data, &arr); jsonErr != nil || len(arr) < 2 {
				continue
			}
			var label string
			if jsonErr := json.Unmarshal(arr[0], &label); jsonErr != nil || label != "EVENT" {
				continue
			}
			var ev struct {
				ID      string `json:"id"`
				Kind    int    `json:"kind"`
				Pubkey  string `json:"pubkey"`
				Content string `json:"content"`
			}
			if jsonErr := json.Unmarshal(arr[1], &ev); jsonErr != nil {
				continue
			}
			r.mu.Lock()
			r.evs = append(r.evs, capturedEvent{Kind: ev.Kind, Pubkey: ev.Pubkey, Content: ev.Content})
			r.mu.Unlock()
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`["OK","`+ev.ID+`",true,""]`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r.url = strings.Replace(srv.URL, "http", "ws", 1)
	return r
}
