package http

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/go-flokicoin/crypto/ecdsa"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/lsps/lsps0"
	"github.com/labstack/echo/v4"
	"github.com/tv42/zbase32"
)

// LSPS5 Webhook Notification Methods (JSON-RPC)
const (
	LSPS5MethodWebhookRegistered          = "lsps5.webhook_registered"
	LSPS5MethodPaymentIncoming            = "lsps5.payment_incoming"
	LSPS5MethodExpirySoon                 = "lsps5.expiry_soon"
	LSPS5MethodLiquidityManagementRequest = "lsps5.liquidity_management_request"
	LSPS5MethodOnionMessageIncoming       = "lsps5.onion_message_incoming"
	LSPS5MethodOrderStateChanged          = "lsps5.order_state_changed"
)

// LSPS5OrderStateChangedParams contains params for lsps5.order_state_changed
type LSPS5OrderStateChangedParams struct {
	OrderID      string  `json:"order_id"`
	State        string  `json:"state"`
	ChannelPoint *string `json:"channel_point,omitempty"`
	Error        *string `json:"error,omitempty"`
}

// LSPS5WebhookNotification represents a JSON-RPC 2.0 notification from an LSP
type LSPS5WebhookNotification struct {
	Jsonrpc string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// LSPS5ExpirySoonParams contains params for lsps5.expiry_soon
type LSPS5ExpirySoonParams struct {
	Timeout uint32 `json:"timeout"`
}

// WebhookCallbackRequest contains the validated webhook callback data
type WebhookCallbackRequest struct {
	LSPPubkey    string
	OrderID      string
	Notification *LSPS5WebhookNotification
	Timestamp    string
	Signature    string
}

// WebhookEventHub manages WebSocket connections for pushing events to frontend
type WebhookEventHub struct {
	clients    map[chan []byte]bool
	register   chan chan []byte
	unregister chan chan []byte
	broadcast  chan []byte
	mu         sync.RWMutex
}

// NewWebhookEventHub creates a new event hub for WebSocket connections
func NewWebhookEventHub() *WebhookEventHub {
	hub := &WebhookEventHub{
		clients:    make(map[chan []byte]bool),
		register:   make(chan chan []byte),
		unregister: make(chan chan []byte),
		broadcast:  make(chan []byte, 256),
	}
	go hub.run()
	return hub
}

func (h *WebhookEventHub) run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client)
			}
			h.mu.Unlock()
		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client <- message:
				default:
					// Client buffer full, skip
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Broadcast sends a message to all connected clients
func (h *WebhookEventHub) Broadcast(message []byte) {
	select {
	case h.broadcast <- message:
	default:
		logger.Logger.Warn().Msg("Webhook event hub broadcast channel full")
	}
}

// lsps5WebhookCallbackHandler handles incoming LSPS5 webhook notifications from LSPs
func (httpSvc *HttpService) lsps5WebhookCallbackHandler(c echo.Context) error {
	// Extract LSP info from query params (encoded by client when registering webhook)
	lspPubkey := c.QueryParam("lsp")
	orderID := c.QueryParam("order")

	if lspPubkey == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: "Missing lsp parameter",
		})
	}

	// Extract LSPS5 headers
	timestamp := c.Request().Header.Get("x-lsps5-timestamp")
	signature := c.Request().Header.Get("x-lsps5-signature")

	if timestamp == "" || signature == "" {
		logger.Logger.Warn().
			Str("lsp", lspPubkey).
			Msg("LSPS5 webhook missing required headers")
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: "Missing x-lsps5-timestamp or x-lsps5-signature headers",
		})
	}

	// Read and parse the notification body
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: "Failed to read request body",
		})
	}

	var notification LSPS5WebhookNotification
	if err := json.Unmarshal(body, &notification); err != nil {
		logger.Logger.Warn().
			Str("lsp", lspPubkey).
			Err(err).
			Msg("Failed to parse LSPS5 webhook notification")
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: "Invalid JSON-RPC notification",
		})
	}

	// Validate JSON-RPC version
	if notification.Jsonrpc != "2.0" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: "Invalid jsonrpc version",
		})
	}

	// Validate signature per LSPS5 spec
	// Message format: "LSPS5: DO NOT SIGN THIS MESSAGE MANUALLY: LSP: At ${timestamp} I notify ${body}"
	messageToVerify := fmt.Sprintf("LSPS5: DO NOT SIGN THIS MESSAGE MANUALLY: LSP: At %s I notify %s", timestamp, string(body))

	// Verify the signature
	valid, recoveredPubkey, err := verifyLSPS5Signature(messageToVerify, signature, lspPubkey)
	if err != nil {
		logger.Logger.Warn().
			Str("lsp", lspPubkey).
			Err(err).
			Msg("LSPS5 webhook signature verification failed")
		// Per security best practices, reject invalid signatures
		return c.JSON(http.StatusUnauthorized, ErrorResponse{
			Message: "Invalid signature",
		})
	}
	if !valid {
		logger.Logger.Warn().
			Str("lsp", lspPubkey).
			Str("recovered_pubkey", recoveredPubkey).
			Msg("LSPS5 webhook signature mismatch - recovered pubkey does not match claimed LSP")
		return c.JSON(http.StatusUnauthorized, ErrorResponse{
			Message: "Signature pubkey mismatch",
		})
	}

	logger.Logger.Debug().
		Str("lsp", lspPubkey).
		Str("method", notification.Method).
		Str("timestamp", timestamp).
		Bool("signature_valid", valid).
		Msg("Received verified LSPS5 webhook notification")

	// Handle the notification based on method
	if err := httpSvc.handleLSPS5Notification(lspPubkey, orderID, &notification); err != nil {
		logger.Logger.Error().
			Str("lsp", lspPubkey).
			Str("method", notification.Method).
			Err(err).
			Msg("Failed to handle LSPS5 notification")
		// Return 200 OK anyway per spec - LSP should not retry based on our handling
	}

	// Return 200 OK as per LSPS5 spec
	return c.NoContent(http.StatusOK)
}

// handleLSPS5Notification processes the notification and broadcasts to connected clients
func (httpSvc *HttpService) handleLSPS5Notification(lspPubkey, orderID string, notification *LSPS5WebhookNotification) error {
	// Create event for broadcasting
	event := &events.Event{
		Event: constants.LSPS5_EVENT_NOTIFICATION,
		Properties: map[string]interface{}{
			"lsp_pubkey": lspPubkey,
			"method":     notification.Method,
			"order_id":   orderID,
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
		},
	}

	switch notification.Method {
	case LSPS5MethodWebhookRegistered:
		logger.Logger.Info().
			Str("lsp", lspPubkey).
			Msg("Webhook successfully registered with LSP")

	case LSPS5MethodPaymentIncoming:
		logger.Logger.Info().
			Str("lsp", lspPubkey).
			Msg("Incoming payment notification received")
		// Trigger wallet sync
		httpSvc.api.SyncWallet()
		event.Event = constants.LSPS5_EVENT_PAYMENT_INCOMING

	case LSPS5MethodExpirySoon:
		var params LSPS5ExpirySoonParams
		if err := json.Unmarshal(notification.Params, &params); err == nil {
			if props, ok := event.Properties.(map[string]interface{}); ok {
				props["timeout_block"] = params.Timeout
			}
		}
		logger.Logger.Warn().
			Str("lsp", lspPubkey).
			Interface("params", params).
			Msg("HTLC expiry soon notification")
		event.Event = constants.LSPS5_EVENT_EXPIRY_SOON

	case LSPS5MethodLiquidityManagementRequest:
		logger.Logger.Info().
			Str("lsp", lspPubkey).
			Msg("LSP requesting liquidity management")
		event.Event = constants.LSPS5_EVENT_LIQUIDITY_REQUEST

	case LSPS5MethodOnionMessageIncoming:
		logger.Logger.Info().
			Str("lsp", lspPubkey).
			Msg("Onion message incoming notification")
		event.Event = constants.LSPS5_EVENT_ONION_MESSAGE

	case LSPS5MethodOrderStateChanged:
		var params LSPS5OrderStateChangedParams
		if err := json.Unmarshal(notification.Params, &params); err == nil {
			if props, ok := event.Properties.(map[string]interface{}); ok {
				props["state"] = params.State
				if params.ChannelPoint != nil {
					props["channel_point"] = *params.ChannelPoint
				}
				if params.Error != nil {
					props["error"] = *params.Error
				}
			}
		}
		logger.Logger.Info().
			Str("lsp", lspPubkey).
			Str("order_id", orderID).
			Interface("params", params).
			Msg("Order state changed notification")
		event.Event = constants.LSPS5_EVENT_ORDER_STATE_CHANGED

		// Update persistent order state
		if err := httpSvc.api.UpdateLSPS1OrderState(context.Background(), orderID, params.State); err != nil {
			logger.Logger.Warn().Err(err).Str("order_id", orderID).Msg("Failed to update persistent order state from webhook")
		}

	default:
		// Check for LSPS1-specific notifications (non-standard but useful)
		if strings.HasPrefix(notification.Method, "lsps1.") {
			event.Event = constants.LSPS1_EVENT_NOTIFICATION
			logger.Logger.Info().
				Str("lsp", lspPubkey).
				Str("method", notification.Method).
				Msg("LSPS1 notification received via webhook")
		} else {
			logger.Logger.Debug().
				Str("lsp", lspPubkey).
				Str("method", notification.Method).
				Msg("Unknown LSPS5 notification method - ignoring")
		}
	}

	// Publish event to internal event system
	httpSvc.eventPublisher.Publish(event)

	return nil
}

// LSPS5WebhookEvent represents an event to be sent via SSE/WebSocket
type LSPS5WebhookEvent struct {
	Type      string                 `json:"type"`
	LSPPubkey string                 `json:"lsp_pubkey"`
	Method    string                 `json:"method"`
	OrderID   string                 `json:"order_id,omitempty"`
	Params    map[string]interface{} `json:"params,omitempty"`
	Timestamp string                 `json:"timestamp"`
}

// lsps5EventsSSEHandler provides Server-Sent Events for LSPS5 notifications
func (httpSvc *HttpService) lsps5EventsSSEHandler(c echo.Context) error {
	logger.Logger.Info().Msg("lsps5EventsSSEHandler connection attempt")

	// Check if flushing is supported
	if _, ok := c.Response().Writer.(http.Flusher); !ok {
		logger.Logger.Error().Msg("Streaming not supported: ResponseWriter does not implement http.Flusher")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{Message: "Streaming not supported by server"})
	}

	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().Header().Set("X-Accel-Buffering", "no")

	// Flush headers immediately
	c.Response().Flush()

	// Create a channel for this client
	eventChan := make(chan *events.Event, 16)

	// Subscribe to LSPS5 events
	subscriber := &lsps5EventSubscriber{
		handler: func(event *events.Event) {
			if strings.HasPrefix(event.Event, "lsps5.") || strings.HasPrefix(event.Event, "lsps1.") {
				select {
				case eventChan <- event:
				default:
					// Channel full, skip event
				}
			}
		},
	}
	httpSvc.eventPublisher.RegisterSubscriber(subscriber)
	defer httpSvc.eventPublisher.RemoveSubscriber(subscriber)

	// Send keepalive and events
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.Request().Context().Done():
			return nil
		case <-ticker.C:
			// Send keepalive comment
			if _, err := c.Response().Write([]byte(": keepalive\n\n")); err != nil {
				return nil
			}
			c.Response().Flush()
		case event := <-eventChan:
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := c.Response().Write([]byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event.Event, string(data)))); err != nil {
				return nil
			}
			c.Response().Flush()
		}
	}
}

type lsps5EventSubscriber struct {
	handler func(event *events.Event)
}

func (s *lsps5EventSubscriber) ConsumeEvent(ctx context.Context, event *events.Event, globalProperties map[string]interface{}) {
	s.handler(event)
}

// Helper functions

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// JSON-RPC notification helper for parsing
func parseJsonRpcNotification(data []byte) (*lsps0.JsonRpcRequest, error) {
	var notification lsps0.JsonRpcRequest
	if err := json.Unmarshal(data, &notification); err != nil {
		return nil, err
	}
	return &notification, nil
}

// verifyLSPS5Signature verifies an LSPS5 webhook signature
// Returns (valid, recoveredPubkey, error)
// The signature is zbase32 encoded and created over the double-SHA256 hash of the message
func verifyLSPS5Signature(message, signatureStr, expectedPubkey string) (bool, string, error) {
	// Decode the zbase32 signature
	sig, err := zbase32.DecodeString(signatureStr)
	if err != nil {
		return false, "", fmt.Errorf("failed to decode zbase32 signature: %w", err)
	}

	// The signature should be 65 bytes (recoverable signature)
	if len(sig) != 65 {
		return false, "", fmt.Errorf("invalid signature length: expected 65, got %d", len(sig))
	}

	// Per LSPS5 spec, the message is hashed with double-SHA256
	// Note: LSPS5 does NOT use the "Lightning Signed Message:" prefix like LND's SignMessage
	// The raw message is hashed directly
	digest := chainhash.DoubleHashB([]byte(message))

	// Recover the public key from the signature
	pubKey, _, err := ecdsa.RecoverCompact(sig, digest)
	if err != nil {
		return false, "", fmt.Errorf("failed to recover public key: %w", err)
	}

	// Convert recovered pubkey to hex string
	recoveredPubkeyHex := hex.EncodeToString(pubKey.SerializeCompressed())

	// Check if the recovered pubkey matches the expected LSP pubkey
	if recoveredPubkeyHex != expectedPubkey {
		return false, recoveredPubkeyHex, nil
	}

	return true, recoveredPubkeyHex, nil
}
