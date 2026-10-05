package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests"
)

// This file covers the hub-side announcement path (newPrivateTransport,
// publishTransportAnnouncement) that nothing previously exercised: every
// existing service-package test built a *privateTransport by hand
// (newUnwrapFixture, newTestPrivateTransport) and never went through the real
// startup code. That gap is exactly what let a cashctl user hit
// ErrNoAnnouncement against a hub on the current commit — see the cases
// below, each one a distinct reason publishTransportAnnouncement can fail.

// announcingLN adds TransportSigner to the test suite's default MockLn, which
// deliberately does not implement it (see lnclient.TransportSigner's own
// doc on why it is kept out of the main interface). Embedding rather than
// reimplementing the other 30-odd LNClient methods.
type announcingLN struct {
	*tests.MockLn
	pub     string
	priv    *btcec.PrivateKey
	signErr error
}

func (a *announcingLN) GetPubkey() string { return a.pub }

// SignSchnorrNodeKey mirrors flnd's real contract: it hashes msg itself before
// signing, which is the exact distinction AnnouncementSigningPayload's doc
// warns about.
func (a *announcingLN) SignSchnorrNodeKey(_ context.Context, msg []byte) ([]byte, error) {
	if a.signErr != nil {
		return nil, a.signErr
	}
	digest := sha256.Sum256(msg)
	sig, err := schnorr.Sign(a.priv, digest[:])
	if err != nil {
		return nil, err
	}
	return sig.Serialize(), nil
}

func newAnnouncingLN(t *testing.T, base *tests.MockLn) *announcingLN {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	return &announcingLN{
		MockLn: base,
		pub:    hex.EncodeToString(priv.PubKey().SerializeCompressed()),
		priv:   priv,
	}
}

// newOKRelay serves a minimal relay that answers every EVENT with OK:true. A
// fake because the point of these tests is publishTransportAnnouncement's own
// logic, not a real relay's storage or validation.
func newOKRelay(t *testing.T) string {
	t.Helper()
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
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
			if err := json.Unmarshal(data, &arr); err != nil || len(arr) < 2 {
				continue
			}
			var label string
			if err := json.Unmarshal(arr[0], &label); err != nil || label != "EVENT" {
				continue
			}
			var ev struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(arr[1], &ev); err != nil {
				continue
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`["OK","`+ev.ID+`",true,""]`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return strings.Replace(srv.URL, "http", "ws", 1)
}

func TestNewPrivateTransport_RequiresLNClient(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	svc := &service{cfg: ts.Cfg, keys: ts.Keys}
	_, err = svc.newPrivateTransport()
	require.ErrorContains(t, err, "private transport needs an LN client")
}

// TestNewPrivateTransport_RejectsWrongPubkeyLength pins the guard against a
// backend whose GetPubkey does not return a 33-byte compressed key: slicing
// off what it assumes is a one-byte prefix would otherwise silently derive
// the wrong node identity instead of failing loudly.
func TestNewPrivateTransport_RejectsWrongPubkeyLength(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := &announcingLN{MockLn: base, pub: "deadbeef"}

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	_, err = svc.newPrivateTransport()
	require.ErrorContains(t, err, "want 66 (compressed)")
}

// TestPublishTransportAnnouncement_RequiresTransportSigner is the scenario a
// cashctl user actually hit: the hub's LN backend cannot sign the
// announcement, so clients that have never talked to this hub see
// ErrNoAnnouncement forever, even though the hub itself is healthy. It also
// pins that the suite's own default fixture (MockLn, used by every other
// service test via tests.CreateTestService) is this exact case — nothing
// using the default fixture alone would ever have caught the gap this test
// closes.
func TestPublishTransportAnnouncement_RequiresTransportSigner(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	// MockLn's own default GetPubkey ("123pubkey") is itself not a valid
	// compressed key, which would otherwise fail at newPrivateTransport and
	// mask the case this test is for — a backend with a perfectly good node
	// identity that simply cannot sign.
	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	base.Pubkey = hex.EncodeToString(priv.PubKey().SerializeCompressed())

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ts.LNClient}
	pt, err := svc.newPrivateTransport()
	require.NoError(t, err)

	pool := nostr.NewSimplePool(context.Background())
	err = svc.publishTransportAnnouncement(context.Background(), pool, pt)
	require.ErrorContains(t, err, "this LN backend cannot sign the transport announcement")

	status := svc.GetPrivateTransportStatus()
	assert.False(t, status.Announced, "GetPrivateTransportStatus must reflect the failure, not just the error return")
	assert.Zero(t, status.AnnouncedRelays)
	assert.Contains(t, status.Error, "this LN backend cannot sign the transport announcement")
}

func TestPublishTransportAnnouncement_PropagatesSignerError(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)
	ln.signErr = errors.New("node locked")

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	pt, err := svc.newPrivateTransport()
	require.NoError(t, err)

	pool := nostr.NewSimplePool(context.Background())
	err = svc.publishTransportAnnouncement(context.Background(), pool, pt)
	require.ErrorContains(t, err, "node refused to sign the announcement")
	require.ErrorContains(t, err, "node locked")

	status := svc.GetPrivateTransportStatus()
	assert.False(t, status.Announced)
	assert.Contains(t, status.Error, "node locked")
}

// TestPublishTransportAnnouncement_NoRelayAcceptedIsAnError covers a hub whose
// configured relays are simply unreachable at startup — a second, distinct
// way to end up unannounced that has nothing to do with the LN backend.
func TestPublishTransportAnnouncement_NoRelayAcceptedIsAnError(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	require.NoError(t, ts.Cfg.SetUpdate("Relay", "ws://127.0.0.1:1", ""))

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	pt, err := svc.newPrivateTransport()
	require.NoError(t, err)

	pool := nostr.NewSimplePool(context.Background())
	err = svc.publishTransportAnnouncement(context.Background(), pool, pt)
	require.ErrorContains(t, err, "no relay accepted the transport announcement")

	status := svc.GetPrivateTransportStatus()
	assert.False(t, status.Announced)
	assert.Zero(t, status.AnnouncedRelays)
	assert.Equal(t, 1, status.TotalRelays)
	assert.Contains(t, status.Error, "no relay accepted the transport announcement")
}

// TestPublishTransportAnnouncement_SucceedsWhenAtLeastOneRelayAccepts proves
// the converse of the previous test: one unreachable relay among several
// configured must not sink the announcement as long as another accepts it.
func TestPublishTransportAnnouncement_SucceedsWhenAtLeastOneRelayAccepts(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	good := newOKRelay(t)
	require.NoError(t, ts.Cfg.SetUpdate("Relay", "ws://127.0.0.1:1,"+good, ""))

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	pt, err := svc.newPrivateTransport()
	require.NoError(t, err)

	pool := nostr.NewSimplePool(context.Background())
	err = svc.publishTransportAnnouncement(context.Background(), pool, pt)
	require.NoError(t, err, "one reachable relay accepting the event must be enough")

	status := svc.GetPrivateTransportStatus()
	assert.True(t, status.Announced)
	assert.Equal(t, 1, status.AnnouncedRelays)
	assert.Equal(t, 2, status.TotalRelays)
	assert.Empty(t, status.Error)
}

// startPrivateTransport itself (resolve -> start the subscription goroutine
// -> announce) is deliberately NOT exercised here. Doing so surfaced a real,
// pre-existing shutdown-ordering bug: watchPrivateSubscription's inner
// `go func(){ for event := range eventsChannel {...} }()` (private_transport.go
// around line 312) is never registered with the errgroup it's handed, so on
// context cancellation the outer call returns immediately while that raw
// goroutine keeps running — and logging via the package-level zerolog
// instance — until eventsChannel itself closes. Under `go test -race` across
// this package, that tail goroutine from one test's startPrivateTransport can
// still be mid-log when the next test's tests.CreateTestService re-runs
// logger.Init, which the race detector (correctly) flags. That bug is a
// production shutdown-logging hazard, not a gap in the announcement logic
// this file targets, so it is reported rather than papered over with a
// sleep; publishTransportAnnouncement's own error above already pins the
// exact message startPrivateTransport would wrap unchanged.
