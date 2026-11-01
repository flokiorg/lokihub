package lsps

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func newLsps5TestServer(cfg Config, mockOS *MockOpeningService, mockNotif *MockNotificationService) *Handlers {
	return NewHandlers(cfg, new(MockLndClient), new(MockNodesService), mockOS, new(MockLiquidityManager), mockNotif, nil)
}

var testPeerBytes, _ = hex.DecodeString("03aabbccddeeff00112233445566778899aabbccddeeff0011223344556677")
var testPeerId = hex.EncodeToString(testPeerBytes)

func setWebhookReq(t *testing.T, appName, webhook, transport string) *JsonRpcRequest {
	t.Helper()
	params, err := json.Marshal(Lsps5SetWebhookRequest{AppName: appName, Webhook: webhook, Transport: transport})
	require.NoError(t, err)
	return &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: params}
}

func TestHandleLsps5SetWebhook_InvalidParams(t *testing.T) {
	server := newLsps5TestServer(Config{}, new(MockOpeningService), new(MockNotificationService))
	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: json.RawMessage(`not-json`)}

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32602, resp.Error.Code)
}

func TestHandleLsps5SetWebhook_MissingFields(t *testing.T) {
	server := newLsps5TestServer(Config{}, new(MockOpeningService), new(MockNotificationService))
	req := setWebhookReq(t, "", "https://example.com", "push")

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32602, resp.Error.Code)
}

func TestHandleLsps5SetWebhook_AppNameTooLong(t *testing.T) {
	server := newLsps5TestServer(Config{}, new(MockOpeningService), new(MockNotificationService))
	longName := make([]byte, Lsps5MaxAppNameBytes+1)
	for i := range longName {
		longName[i] = 'a'
	}
	req := setWebhookReq(t, string(longName), "https://example.com", "push")

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, Lsps5ErrorTooLong, resp.Error.Code)
}

func TestHandleLsps5SetWebhook_WebhookTooLong(t *testing.T) {
	server := newLsps5TestServer(Config{}, new(MockOpeningService), new(MockNotificationService))
	longUrl := "https://example.com/" + string(make([]byte, Lsps5MaxWebhookChars))
	req := setWebhookReq(t, "app", longUrl, "push")

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, Lsps5ErrorTooLong, resp.Error.Code)
}

func TestHandleLsps5SetWebhook_PushDisabled(t *testing.T) {
	server := newLsps5TestServer(Config{EnableLsps5Https: false}, new(MockOpeningService), new(MockNotificationService))
	req := setWebhookReq(t, "app", "https://example.com", "push")

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, Lsps5ErrorUnsupportedProtocol, resp.Error.Code)
}

func TestHandleLsps5SetWebhook_PushInvalidUrl(t *testing.T) {
	server := newLsps5TestServer(Config{EnableLsps5Https: true}, new(MockOpeningService), new(MockNotificationService))
	req := setWebhookReq(t, "app", "http://not-https.example", "push")

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, Lsps5ErrorUnsupportedProtocol, resp.Error.Code, "a non-https, non-localhost URL's error message contains 'unsupported_protocol', which set_webhook maps to this specific code rather than the generic parse-error one")
}

func TestHandleLsps5SetWebhook_NostrDisabled(t *testing.T) {
	server := newLsps5TestServer(Config{EnableLsps5Nostr: false}, new(MockOpeningService), new(MockNotificationService))
	req := setWebhookReq(t, "app", "nostr+walletconnect://irrelevant", "nostr")

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, Lsps5ErrorUnsupportedProtocol, resp.Error.Code)
}

func TestHandleLsps5SetWebhook_NostrInvalidUri(t *testing.T) {
	server := newLsps5TestServer(Config{EnableLsps5Nostr: true}, new(MockOpeningService), new(MockNotificationService))
	req := setWebhookReq(t, "app", "not-a-valid-nostr-uri", "nostr")

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, Lsps5ErrorUrlParseError, resp.Error.Code)
}

func TestHandleLsps5SetWebhook_UnsupportedTransport(t *testing.T) {
	server := newLsps5TestServer(Config{}, new(MockOpeningService), new(MockNotificationService))
	req := setWebhookReq(t, "app", "https://example.com", "carrier-pigeon")

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32602, resp.Error.Code)
}

func TestHandleLsps5SetWebhook_TooManyWebhooks(t *testing.T) {
	mockOS := new(MockOpeningService)
	mockOS.On("GetLsps5Webhooks", testPeerId).Return([]*core.Lsps5Webhook{
		{AppName: "existing-1"}, {AppName: "existing-2"},
	}, nil)
	server := newLsps5TestServer(Config{EnableLsps5Https: true, Lsps5MaxWebhooks: 2}, mockOS, new(MockNotificationService))
	req := setWebhookReq(t, "new-app", "https://example.com", "push")

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, Lsps5ErrorTooManyWebhooks, resp.Error.Code)
}

// TestHandleLsps5SetWebhook_NoChange_SkipsRegisterAndNotify covers the
// dedup branch: re-submitting the same app_name/url/transport a client
// already has registered must report no_change=true and skip both the DB
// write and the webhook_registered notification.
func TestHandleLsps5SetWebhook_NoChange_SkipsRegisterAndNotify(t *testing.T) {
	mockOS := new(MockOpeningService)
	mockOS.On("GetLsps5Webhooks", testPeerId).Return([]*core.Lsps5Webhook{
		{AppName: "app", Url: "https://example.com", Transport: "push"},
	}, nil)
	mockNotif := new(MockNotificationService)
	server := newLsps5TestServer(Config{EnableLsps5Https: true, Lsps5MaxWebhooks: 10}, mockOS, mockNotif)
	req := setWebhookReq(t, "app", "https://example.com", "push")

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)

	require.Nil(t, resp.Error)
	result, ok := resp.Result.(Lsps5SetWebhookResponse)
	require.True(t, ok)
	assert.True(t, result.NoChange)
	mockOS.AssertNotCalled(t, "RegisterLsps5Webhook", mock.Anything)
	mockNotif.AssertNotCalled(t, "NotifyWebhookRegistered", mock.Anything, mock.Anything)
}

func TestHandleLsps5SetWebhook_Success_RegistersAndNotifies(t *testing.T) {
	mockOS := new(MockOpeningService)
	mockOS.On("GetLsps5Webhooks", testPeerId).Return([]*core.Lsps5Webhook{}, nil)
	mockOS.On("RegisterLsps5Webhook", mock.MatchedBy(func(w *core.Lsps5Webhook) bool {
		return w.ClientNodeId == testPeerId && w.AppName == "app" && w.Url == "https://example.com" && w.Transport == "push"
	})).Return(nil)
	mockNotif := new(MockNotificationService)
	mockNotif.On("NotifyWebhookRegistered", testPeerId, mock.Anything).Return(true).Maybe()

	server := newLsps5TestServer(Config{EnableLsps5Https: true, Lsps5MaxWebhooks: 10}, mockOS, mockNotif)
	req := setWebhookReq(t, "app", "https://example.com", "push")

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)

	require.Nil(t, resp.Error)
	result, ok := resp.Result.(Lsps5SetWebhookResponse)
	require.True(t, ok)
	assert.False(t, result.NoChange)
	assert.Equal(t, 1, result.NumWebhooks)
	mockOS.AssertCalled(t, "RegisterLsps5Webhook", mock.Anything)
}

func TestHandleLsps5SetWebhook_RegisterError(t *testing.T) {
	mockOS := new(MockOpeningService)
	mockOS.On("GetLsps5Webhooks", testPeerId).Return([]*core.Lsps5Webhook{}, nil)
	mockOS.On("RegisterLsps5Webhook", mock.Anything).Return(assert.AnError)
	server := newLsps5TestServer(Config{EnableLsps5Https: true, Lsps5MaxWebhooks: 10}, mockOS, new(MockNotificationService))
	req := setWebhookReq(t, "app", "https://example.com", "push")

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32603, resp.Error.Code)
}

func TestHandleLsps5ListWebhooks(t *testing.T) {
	mockOS := new(MockOpeningService)
	mockOS.On("GetLsps5Webhooks", testPeerId).Return([]*core.Lsps5Webhook{
		{AppName: "wallet-a"}, {AppName: "wallet-b"},
	}, nil)
	server := newLsps5TestServer(Config{Lsps5MaxWebhooks: 10}, mockOS, new(MockNotificationService))
	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`)}

	resp := server.handleLsps5ListWebhooks(req, testPeerBytes)

	require.Nil(t, resp.Error)
	result, ok := resp.Result.(Lsps5ListWebhooksResponse)
	require.True(t, ok)
	assert.Equal(t, []string{"wallet-a", "wallet-b"}, result.AppNames)
	assert.Equal(t, 10, result.MaxWebhooks)
}

func TestHandleLsps5ListWebhooks_StoreError(t *testing.T) {
	mockOS := new(MockOpeningService)
	mockOS.On("GetLsps5Webhooks", testPeerId).Return(nil, assert.AnError)
	server := newLsps5TestServer(Config{}, mockOS, new(MockNotificationService))
	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`)}

	resp := server.handleLsps5ListWebhooks(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32603, resp.Error.Code)
}

func TestHandleLsps5RemoveWebhook_InvalidParams(t *testing.T) {
	server := newLsps5TestServer(Config{}, new(MockOpeningService), new(MockNotificationService))
	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: json.RawMessage(`not-json`)}

	resp := server.handleLsps5RemoveWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32602, resp.Error.Code)
}

func TestHandleLsps5RemoveWebhook_MissingAppName(t *testing.T) {
	server := newLsps5TestServer(Config{}, new(MockOpeningService), new(MockNotificationService))
	params, _ := json.Marshal(Lsps5RemoveWebhookRequest{AppName: ""})
	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: params}

	resp := server.handleLsps5RemoveWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32602, resp.Error.Code)
}

func TestHandleLsps5RemoveWebhook_Success(t *testing.T) {
	mockOS := new(MockOpeningService)
	mockOS.On("RemoveLsps5Webhook", testPeerId, "app").Return(nil)
	server := newLsps5TestServer(Config{}, mockOS, new(MockNotificationService))
	params, _ := json.Marshal(Lsps5RemoveWebhookRequest{AppName: "app"})
	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: params}

	resp := server.handleLsps5RemoveWebhook(req, testPeerBytes)
	require.Nil(t, resp.Error)
}

func TestHandleLsps5RemoveWebhook_NotFound(t *testing.T) {
	mockOS := new(MockOpeningService)
	mockOS.On("RemoveLsps5Webhook", testPeerId, "app").Return(assert.AnError)
	server := newLsps5TestServer(Config{}, mockOS, new(MockNotificationService))
	params, _ := json.Marshal(Lsps5RemoveWebhookRequest{AppName: "app"})
	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: params}

	resp := server.handleLsps5RemoveWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, Lsps5ErrorAppNameNotFound, resp.Error.Code)
}

// set_webhook resolves a client-supplied hostname, writes to the DB and
// fires an outbound delivery, so it must sit behind the same per-peer burst
// limiter as lsps1.create_order and lsps2.buy, checked before any of that
// work. Push is disabled here so every allowed call stops at a cheap,
// distinct error code, and the store is never touched.
func TestHandleLsps5SetWebhook_BurstLimitExceeded_RejectsBeforeAnyWork(t *testing.T) {
	mockOS := new(MockOpeningService)
	server := newLsps5TestServer(Config{EnableLsps5Https: false}, mockOS, new(MockNotificationService))
	req := setWebhookReq(t, "app", "https://example.com", "push")

	for i := range defaultBurstLimit {
		resp := server.handleLsps5SetWebhook(req, testPeerBytes)
		require.NotNil(t, resp.Error)
		require.Equal(t, Lsps5ErrorUnsupportedProtocol, resp.Error.Code, "call %d should be within the burst allowance", i+1)
	}

	resp := server.handleLsps5SetWebhook(req, testPeerBytes)
	require.NotNil(t, resp.Error, "the call exceeding the burst allowance must be rejected")
	assert.Equal(t, -32603, resp.Error.Code)

	other, _ := hex.DecodeString("02aabbccddeeff00112233445566778899aabbccddeeff0011223344556677")
	otherResp := server.handleLsps5SetWebhook(req, other)
	require.NotNil(t, otherResp.Error)
	assert.Equal(t, Lsps5ErrorUnsupportedProtocol, otherResp.Error.Code, "a different peer must not be affected")
	mockOS.AssertNotCalled(t, "GetLsps5Webhooks", mock.Anything)
}

// Loopback http webhooks are a development convenience: a node without the
// dev flag must refuse them, and the flag must restore them.
func TestHandleLsps5SetWebhook_LoopbackHTTP_RequiresDevFlag(t *testing.T) {
	server := newLsps5TestServer(Config{EnableLsps5Https: true, Lsps5MaxWebhooks: 10}, new(MockOpeningService), new(MockNotificationService))
	resp := server.handleLsps5SetWebhook(setWebhookReq(t, "app", "http://127.0.0.1:9000/hook", "push"), testPeerBytes)
	require.NotNil(t, resp.Error)
	assert.Equal(t, Lsps5ErrorUnsupportedProtocol, resp.Error.Code)

	mockOS := new(MockOpeningService)
	mockOS.On("GetLsps5Webhooks", testPeerId).Return([]*core.Lsps5Webhook{}, nil)
	mockOS.On("RegisterLsps5Webhook", mock.Anything).Return(nil)
	mockNotif := new(MockNotificationService)
	mockNotif.On("NotifyWebhookRegistered", testPeerId, mock.Anything).Return(true).Maybe()
	dev := newLsps5TestServer(Config{EnableLsps5Https: true, Lsps5MaxWebhooks: 10, Lsps5AllowLoopbackWebhooks: true}, mockOS, mockNotif)
	resp = dev.handleLsps5SetWebhook(setWebhookReq(t, "app", "http://127.0.0.1:9000/hook", "push"), testPeerBytes)
	require.Nil(t, resp.Error)
	mockOS.AssertCalled(t, "RegisterLsps5Webhook", mock.Anything)
}
