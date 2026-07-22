package http

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/crypto/ecdsa"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/lsps/persist"
	"github.com/flokiorg/lokihub/tests/db"
	"github.com/flokiorg/lokihub/tests/mocks"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tv42/zbase32"
)

// ========================
// LSPS5 Webhook Callback Tests
// ========================

// Helper to create a fully configured HttpService for testing
func createTestHttpService(t *testing.T, needsLNClient bool) (*HttpService, chan *events.Event) {
	logger.Init(strconv.Itoa(4))

	gormDb, err := db.NewDB(t)
	require.NoError(t, err)
	t.Cleanup(func() { db.CloseDB(gormDb) })

	mockEventPublisher := events.NewEventPublisher()
	receivedEvents := make(chan *events.Event, 10)
	mockEventPublisher.RegisterSubscriber(&testEventSubscriber{eventChan: receivedEvents})

	mockSvc := mocks.NewMockService(t)
	mockConfig := mocks.NewMockConfig(t)
	mockConfig.On("GetEnv").Return(&config.AppConfig{})

	mockLokiSvc := mocks.NewMockLokiService(t)

	// If SyncWallet will be called, we need a mock LNClient
	if needsLNClient {
		mockLNClient := mocks.NewMockLNClient(t)
		mockLNClient.On("UpdateLastWalletSyncRequest").Maybe()
		mockSvc.On("GetLNClient").Return(mockLNClient, nil).Maybe()
	}

	mockSvc.On("GetDB").Return(gormDb)
	mockSvc.On("GetConfig").Return(mockConfig)
	mockSvc.On("GetKeys").Return(mocks.NewMockKeys(t))
	mockSvc.On("GetLokiSvc").Return(mockLokiSvc)
	mockSvc.On("GetAppStoreSvc").Return(&mocks.MockAppStoreService{})

	httpSvc := NewHttpService(mockSvc, mockEventPublisher)

	return httpSvc, receivedEvents
}

// registerTestLSP inserts pubkey directly into the lsps table so it passes
// lsps5WebhookCallbackHandler's registered-LSP check — the lightweight
// equivalent of the real admin-authenticated "add LSP" flow, without needing
// a full LiquidityManager/peer-connection in these unit tests.
func registerTestLSP(t *testing.T, httpSvc *HttpService, pubkey string) {
	t.Helper()
	require.NoError(t, httpSvc.db.Create(&persist.LSP{
		Pubkey: pubkey,
		Host:   "test.invalid:9735",
		Name:   "Test LSP",
	}).Error)
}

// Helper to generate a valid LSPS5 signature
func generateLSPS5Signature(t *testing.T, privKey *btcec.PrivateKey, timestamp string, body []byte) string {
	message := fmt.Sprintf("LSPS5: DO NOT SIGN THIS MESSAGE MANUALLY: LSP: At %s I notify %s", timestamp, string(body))
	digest := chainhash.DoubleHashB([]byte(message))

	// Sign using compact signature (65 bytes)
	sigBytes := ecdsa.SignCompact(privKey, digest, true)

	return zbase32.EncodeToString(sigBytes)
}

func TestLSPS5WebhookCallback_MissingLspParam(t *testing.T) {
	e := echo.New()
	httpSvc, _ := createTestHttpService(t, false)

	// Generate a random LSP key
	privKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	// lspPubkey := hex.EncodeToString(privKey.PubKey().SerializeCompressed())

	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	reqBody := `{"jsonrpc":"2.0","method":"lsps5.payment_incoming","params":{}}`

	// Valid signature but missing param
	sig := generateLSPS5Signature(t, privKey, timestamp, []byte(reqBody))

	req := httptest.NewRequest(http.MethodPost, "/api/lsps5/webhook-callback", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-lsps5-timestamp", timestamp)
	req.Header.Set("x-lsps5-signature", sig)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	err = httpSvc.lsps5WebhookCallbackHandler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp ErrorResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	assert.Contains(t, resp.Message, "Missing lsp parameter")
}

func TestLSPS5WebhookCallback_MissingHeaders(t *testing.T) {
	e := echo.New()
	httpSvc, _ := createTestHttpService(t, false)

	// Generate random key
	privKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	lspPubkey := hex.EncodeToString(privKey.PubKey().SerializeCompressed())

	reqBody := `{"jsonrpc":"2.0","method":"lsps5.payment_incoming","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "/api/lsps5/webhook-callback?lsp="+lspPubkey, bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	// Missing x-lsps5-timestamp and x-lsps5-signature
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	err = httpSvc.lsps5WebhookCallbackHandler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp ErrorResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	assert.Contains(t, resp.Message, "x-lsps5-timestamp")
}

func TestLSPS5WebhookCallback_InvalidJSON(t *testing.T) {
	e := echo.New()
	httpSvc, _ := createTestHttpService(t, false)

	privKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	lspPubkey := hex.EncodeToString(privKey.PubKey().SerializeCompressed())
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	req := httptest.NewRequest(http.MethodPost, "/api/lsps5/webhook-callback?lsp="+lspPubkey, bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-lsps5-timestamp", timestamp)
	req.Header.Set("x-lsps5-signature", "irrelevant") // Body isn't read successfully to verify signature yet or falls before
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	err = httpSvc.lsps5WebhookCallbackHandler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp ErrorResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	assert.Contains(t, resp.Message, "Invalid JSON-RPC notification")
}

func TestLSPS5WebhookCallback_InvalidJsonRpcVersion(t *testing.T) {
	e := echo.New()
	httpSvc, _ := createTestHttpService(t, false)

	privKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	lspPubkey := hex.EncodeToString(privKey.PubKey().SerializeCompressed())
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	// Invalid jsonrpc version
	reqBody := `{"jsonrpc":"1.0","method":"lsps5.payment_incoming","params":{}}`
	sig := generateLSPS5Signature(t, privKey, timestamp, []byte(reqBody))

	req := httptest.NewRequest(http.MethodPost, "/api/lsps5/webhook-callback?lsp="+lspPubkey, bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-lsps5-timestamp", timestamp)
	req.Header.Set("x-lsps5-signature", sig)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	err = httpSvc.lsps5WebhookCallbackHandler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp ErrorResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	assert.Contains(t, resp.Message, "Invalid jsonrpc version")
}

func TestLSPS5WebhookCallback_UnknownMethodReturns200(t *testing.T) {
	e := echo.New()
	httpSvc, _ := createTestHttpService(t, false)

	privKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	lspPubkey := hex.EncodeToString(privKey.PubKey().SerializeCompressed())
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	// Unknown method
	registerTestLSP(t, httpSvc, lspPubkey)

	reqBody := `{"jsonrpc":"2.0","method":"lsps99.unknown","params":{}}`
	sig := generateLSPS5Signature(t, privKey, timestamp, []byte(reqBody))

	req := httptest.NewRequest(http.MethodPost, "/api/lsps5/webhook-callback?lsp="+lspPubkey, bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-lsps5-timestamp", timestamp)
	req.Header.Set("x-lsps5-signature", sig)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	err = httpSvc.lsps5WebhookCallbackHandler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestLSPS5WebhookCallback_ExpirySoonReturns200(t *testing.T) {
	e := echo.New()
	httpSvc, _ := createTestHttpService(t, false)

	privKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	lspPubkey := hex.EncodeToString(privKey.PubKey().SerializeCompressed())
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	registerTestLSP(t, httpSvc, lspPubkey)

	reqBody := `{"jsonrpc":"2.0","method":"lsps5.expiry_soon","params":{"timeout":144}}`
	sig := generateLSPS5Signature(t, privKey, timestamp, []byte(reqBody))

	req := httptest.NewRequest(http.MethodPost, "/api/lsps5/webhook-callback?lsp="+lspPubkey, bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-lsps5-timestamp", timestamp)
	req.Header.Set("x-lsps5-signature", sig)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	err = httpSvc.lsps5WebhookCallbackHandler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestLSPS5WebhookCallback_WebhookRegisteredReturns200(t *testing.T) {
	e := echo.New()
	httpSvc, _ := createTestHttpService(t, false)

	privKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	lspPubkey := hex.EncodeToString(privKey.PubKey().SerializeCompressed())
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	registerTestLSP(t, httpSvc, lspPubkey)

	reqBody := `{"jsonrpc":"2.0","method":"lsps5.webhook_registered","params":{}}`
	sig := generateLSPS5Signature(t, privKey, timestamp, []byte(reqBody))

	req := httptest.NewRequest(http.MethodPost, "/api/lsps5/webhook-callback?lsp="+lspPubkey, bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-lsps5-timestamp", timestamp)
	req.Header.Set("x-lsps5-signature", sig)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	err = httpSvc.lsps5WebhookCallbackHandler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestLSPS5WebhookCallback_LiquidityRequestReturns200(t *testing.T) {
	e := echo.New()
	httpSvc, _ := createTestHttpService(t, false)

	privKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	lspPubkey := hex.EncodeToString(privKey.PubKey().SerializeCompressed())
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	registerTestLSP(t, httpSvc, lspPubkey)

	reqBody := `{"jsonrpc":"2.0","method":"lsps5.liquidity_management_request","params":{}}`
	sig := generateLSPS5Signature(t, privKey, timestamp, []byte(reqBody))

	req := httptest.NewRequest(http.MethodPost, "/api/lsps5/webhook-callback?lsp="+lspPubkey, bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-lsps5-timestamp", timestamp)
	req.Header.Set("x-lsps5-signature", sig)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	err = httpSvc.lsps5WebhookCallbackHandler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestLSPS5WebhookCallback_UnregisteredLSP_Rejected is the regression test for
// the fix: a well-formed, validly-signed notification from a pubkey nobody
// added as an LSP must be rejected, not silently accepted — previously any
// freshly generated keypair (anyone can make one) would pass, because the
// signature only proves self-consistency ("this claimed pubkey signed this
// message"), not that the claimed pubkey is an LSP this hub actually trusts.
func TestLSPS5WebhookCallback_UnregisteredLSP_Rejected(t *testing.T) {
	e := echo.New()
	httpSvc, _ := createTestHttpService(t, false)

	// Deliberately NOT calling registerTestLSP — this pubkey is a fresh,
	// never-added keypair, exactly what an attacker would use.
	privKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	lspPubkey := hex.EncodeToString(privKey.PubKey().SerializeCompressed())
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	reqBody := `{"jsonrpc":"2.0","method":"lsps5.webhook_registered","params":{}}`
	sig := generateLSPS5Signature(t, privKey, timestamp, []byte(reqBody))

	req := httptest.NewRequest(http.MethodPost, "/api/lsps5/webhook-callback?lsp="+lspPubkey, bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-lsps5-timestamp", timestamp)
	req.Header.Set("x-lsps5-signature", sig)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	err = httpSvc.lsps5WebhookCallbackHandler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	var resp ErrorResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	assert.Contains(t, resp.Message, "not registered")
}

// TestLSPS5WebhookCallback_OrderStateChanged_WrongLSP_NotApplied is the
// regression test for the order-ownership half of the fix: a registered LSP
// (call it LSP B) must not be able to forge a state transition for an order
// that actually belongs to a different LSP (LSP A) — the notification is
// signed correctly and B is a real, trusted LSP, but B doesn't own this
// specific order. UpdateLSPS1OrderState must reject the mismatch, so the
// order's persisted state must be unchanged afterward.
func TestLSPS5WebhookCallback_OrderStateChanged_WrongLSP_NotApplied(t *testing.T) {
	e := echo.New()
	httpSvc, _ := createTestHttpService(t, false)

	ownerPrivKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	ownerPubkey := hex.EncodeToString(ownerPrivKey.PubKey().SerializeCompressed())

	impostorPrivKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	impostorPubkey := hex.EncodeToString(impostorPrivKey.PubKey().SerializeCompressed())

	// Both pubkeys are registered, real LSPs — the attack here isn't an
	// unregistered identity, it's one real LSP claiming another real LSP's order.
	registerTestLSP(t, httpSvc, ownerPubkey)
	registerTestLSP(t, httpSvc, impostorPubkey)

	const orderID = "order-owned-by-owner-pubkey"
	require.NoError(t, httpSvc.db.Create(&persist.LSPS1Order{
		OrderID:   orderID,
		LSPPubkey: ownerPubkey,
		State:     "CREATED",
	}).Error)

	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	reqBody := `{"jsonrpc":"2.0","method":"lsps5.order_state_changed","params":{"order_id":"` + orderID + `","state":"COMPLETED"}}`
	sig := generateLSPS5Signature(t, impostorPrivKey, timestamp, []byte(reqBody))

	req := httptest.NewRequest(http.MethodPost, "/api/lsps5/webhook-callback?lsp="+impostorPubkey+"&order="+orderID, bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-lsps5-timestamp", timestamp)
	req.Header.Set("x-lsps5-signature", sig)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	err = httpSvc.lsps5WebhookCallbackHandler(c)
	require.NoError(t, err)
	// Per the LSPS5 spec, the endpoint still returns 200 even when handling
	// the notification's *content* fails internally (the LSP shouldn't retry
	// based on our handling) — the security property to assert is that the
	// order's state was never actually changed, not the HTTP status.
	assert.Equal(t, http.StatusOK, rec.Code)

	var order persist.LSPS1Order
	require.NoError(t, httpSvc.db.First(&order, "order_id = ?", orderID).Error)
	assert.Equal(t, "CREATED", order.State, "a registered LSP must not be able to change the state of an order it doesn't own")
}

// ========================
// Helper Types
// ========================

type testEventSubscriber struct {
	eventChan chan *events.Event
}

func (s *testEventSubscriber) ConsumeEvent(ctx context.Context, event *events.Event, globalProperties map[string]interface{}) {
	select {
	case s.eventChan <- event:
	default:
	}
}
