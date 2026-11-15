package dashboard

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/flokiorg/lokihub/lspd/transport/auth"
	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubLndClient embeds lightning.Client (nil) so it only needs to
// implement the one method /info's tests actually exercise; calling
// anything else on it would nil-panic, which is fine since nothing else is
// reachable from ServeHTTP's /info path.
type stubLndClient struct {
	lightning.Client
	err error
}

func (s *stubLndClient) GetInfo() (*lightning.GetInfoResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &lightning.GetInfoResult{}, nil
}

// stubNodesService is a minimal ports.NodesService: /info's tests only
// care about how many nodes come back and their reachability, not real
// node-lookup or capacity behavior.
type stubNodesService struct {
	nodes []*core.Node
}

func (s *stubNodesService) GetNode(token string) (*core.Node, error) { return nil, nil }
func (s *stubNodesService) GetNodes() []*core.Node                   { return s.nodes }
func (s *stubNodesService) GetMinChannelCapacity() uint64            { return 0 }

// buildAuthHeader mirrors transport/auth's own (unexported, so
// unreachable from here) test helper of the same shape -- constructing a
// real, validly-signed NIP-98 event is the only way to exercise
// ServeHTTP's actual auth-gated paths, not just the ones that reject
// before ever calling auth.VerifyNostrAuth.
func buildAuthHeader(t *testing.T, sk, method, url, payloadHash string) string {
	t.Helper()
	tags := nostr.Tags{
		{"u", url},
		{"m", method},
	}
	if payloadHash != "" {
		tags = append(tags, nostr.Tag{"payload", payloadHash})
	}
	event := nostr.Event{
		Kind:      auth.KindHttpAuth,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      tags,
		Content:   "",
	}
	require.NoError(t, event.Sign(sk))
	eventJson, err := event.MarshalJSON()
	require.NoError(t, err)
	return "Nostr " + base64.StdEncoding.EncodeToString(eventJson)
}

func newTestKeypair(t *testing.T) (sk, pk string) {
	t.Helper()
	sk = nostr.GeneratePrivateKey()
	pk, err := nostr.GetPublicKey(sk)
	require.NoError(t, err)
	return sk, pk
}

// stubRouter is a minimal Router: these tests are about the transport's
// own behavior (routing, auth, the admin-pubkey gate, envelope
// marshal/unmarshal), not the real dashboard business logic
// (dashboard.Handlers' own tests cover that).
type stubRouter struct {
	resp *lsps.JsonRpcResponse
}

func (s *stubRouter) Route(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	return s.resp
}

func TestServeHTTP_HealthzBypassesAuth(t *testing.T) {
	srv := NewServer("adminpubkey", &stubRouter{}, &stubNodesService{})
	r := httptest.NewRequest(http.MethodGet, "http://example.com/healthz", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "ok", body["status"])
}

// TestServeHTTP_Info_BypassesAuthAndReportsDashboardState covers the one
// thing the frontend actually needs /info for: knowing without any
// credentials whether the dashboard is usable at all, plus per-node
// reachability, so it can disable the login form and show why instead of a
// login attempt that's guaranteed to fail.
func TestServeHTTP_Info_BypassesAuthAndReportsDashboardState(t *testing.T) {
	unreachable := &core.Node{Client: &stubLndClient{err: fmt.Errorf("connection refused")}}
	unreachable.NodeConfig = config.NewNodeConfigRef(&config.NodeConfig{Name: "offline-node"})

	srv := NewServer("", &stubRouter{}, &stubNodesService{nodes: []*core.Node{unreachable}})
	r := httptest.NewRequest(http.MethodGet, "http://example.com/info", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	var body InfoResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.False(t, body.DashboardEnabled, "adminPubKey unset -- dashboard should report disabled")
	require.Len(t, body.Nodes, 1)
	assert.Equal(t, "offline-node", body.Nodes[0].Name)
	assert.False(t, body.Nodes[0].Connected)
}

func TestServeHTTP_MissingAuthHeader_Returns401(t *testing.T) {
	srv := NewServer("adminpubkey", &stubRouter{}, &stubNodesService{})
	r := httptest.NewRequest(http.MethodPost, "http://example.com/dashboard", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestServeHTTP_ValidAuth_EmptyAdminPubkey_Returns403(t *testing.T) {
	sk, _ := newTestKeypair(t)
	srv := NewServer("", &stubRouter{}, &stubNodesService{}) // dashboard "disabled" -- no admin configured

	url := "http://example.com/dashboard"
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.Header.Set("Authorization", buildAuthHeader(t, sk, http.MethodGet, url, ""))
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, r)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestServeHTTP_ValidAuth_WrongPubkey_Returns403(t *testing.T) {
	sk, _ := newTestKeypair(t) // signer's pubkey != the configured admin
	srv := NewServer("someone-elses-pubkey", &stubRouter{}, &stubNodesService{})

	url := "http://example.com/dashboard"
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.Header.Set("Authorization", buildAuthHeader(t, sk, http.MethodGet, url, ""))
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, r)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestServeHTTP_ValidRequest_DispatchesToRouterAndReturnsResult is the
// full round trip: NIP-98 auth -> admin-pubkey authorization -> JSON-RPC
// decode -> Router.Route -> JSON-RPC encode. Proves the one seam this
// split introduced (ServeHTTP delegating to an injected Router instead of
// a giant method-name switch on itself) actually works end to end.
func TestServeHTTP_ValidRequest_DispatchesToRouterAndReturnsResult(t *testing.T) {
	sk, pk := newTestKeypair(t)
	router := &stubRouter{resp: lsps.NewJsonRpcResponse(json.RawMessage(`1`), map[string]string{"ok": "true"})}
	srv := NewServer(pk, router, &stubNodesService{})

	url := "http://example.com/dashboard"
	body := `{"jsonrpc":"2.0","method":"get_summary","id":1}`
	payloadHash := auth.ComputePayloadHash([]byte(body))

	r := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	r.Header.Set("Authorization", buildAuthHeader(t, sk, http.MethodPost, url, payloadHash))
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	var resp lsps.JsonRpcResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Nil(t, resp.Error)
	result, ok := resp.Result.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "true", result["ok"])
}

func TestServeHTTP_GetRequest_MethodNotAllowed(t *testing.T) {
	sk, pk := newTestKeypair(t)
	srv := NewServer(pk, &stubRouter{}, &stubNodesService{})

	url := "http://example.com/dashboard"
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.Header.Set("Authorization", buildAuthHeader(t, sk, http.MethodGet, url, ""))
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, r)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// TestHub_BroadcastDeliversToRegisteredClients exercises the Hub/Client
// pub-sub machinery directly rather than through a real websocket
// connection (handleWS's own upgrade is integration-test territory, not
// worth a fake net.Conn here) -- register drives through the same
// channel-select loop Run() uses in production.
func TestHub_BroadcastDeliversToRegisteredClients(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	client := &Client{hub: hub, send: make(chan []byte, 1)}
	hub.register <- client

	hub.Broadcast(map[string]string{"type": "test_event"})

	select {
	case msg := <-client.send:
		assert.Contains(t, string(msg), "test_event")
	case <-time.After(time.Second):
		t.Fatal("broadcast message never reached the registered client")
	}
}

// TestBroadcastEvent_PublishesThroughHub covers Server.BroadcastEvent's
// own envelope (type/data/ts) on top of Hub.Broadcast, which the test
// above already covers structurally.
func TestBroadcastEvent_PublishesThroughHub(t *testing.T) {
	srv := NewServer("adminpubkey", &stubRouter{}, &stubNodesService{})
	client := &Client{hub: srv.hub, send: make(chan []byte, 1)}
	srv.hub.register <- client

	srv.BroadcastEvent("order_state_changed", map[string]string{"order_id": "abc"})

	select {
	case msg := <-client.send:
		var decoded map[string]interface{}
		require.NoError(t, json.Unmarshal(msg, &decoded))
		assert.Equal(t, "order_state_changed", decoded["type"])
		data, ok := decoded["data"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "abc", data["order_id"])
	case <-time.After(time.Second):
		t.Fatal("BroadcastEvent's message never reached the registered client")
	}
}
