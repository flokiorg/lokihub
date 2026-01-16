// Package lsps2 implements LSPS2 (JIT Channels) client
package lsps2

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/lsps0"
	"github.com/flokiorg/lokihub/lsps/transport"
)

// ClientHandler handles LSPS2 client-side operations
type ClientHandler struct {
	transport    transport.Transport
	eventQueue   *events.EventQueue
	perPeerState map[string]*PeerState
	mu           sync.RWMutex
}

// PendingRequest tracks metadata for cleanup
type PendingRequest struct {
	CreatedAt time.Time
}

// PeerState tracks pending requests for a peer
type PeerState struct {
	PendingGetInfoRequests map[string]PendingRequest
	PendingBuyRequests     map[string]*InboundJITChannel
	mu                     sync.Mutex
}

// InboundJITChannel tracks a pending JIT channel request
type InboundJITChannel struct {
	PaymentSizeMloki *uint64
	CreatedAt        time.Time
}

// NewClientHandler creates a new LSPS2 client handler
func NewClientHandler(transport transport.Transport, eventQueue *events.EventQueue) *ClientHandler {
	return &ClientHandler{
		transport:    transport,
		eventQueue:   eventQueue,
		perPeerState: make(map[string]*PeerState),
	}
}

// generateRequestID generates a random request ID
func generateRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// RequestOpeningParams requests JIT channel opening parameters from the LSP
func (h *ClientHandler) RequestOpeningParams(ctx context.Context, lspPubkey string, token *string) (string, error) {
	requestID := generateRequestID()

	h.mu.Lock()
	if _, ok := h.perPeerState[lspPubkey]; !ok {
		h.perPeerState[lspPubkey] = &PeerState{
			PendingGetInfoRequests: make(map[string]PendingRequest),
			PendingBuyRequests:     make(map[string]*InboundJITChannel),
		}
	}
	peerState := h.perPeerState[lspPubkey]
	h.mu.Unlock()

	peerState.mu.Lock()
	peerState.PendingGetInfoRequests[requestID] = PendingRequest{CreatedAt: time.Now()}
	peerState.mu.Unlock()

	request := &lsps0.JsonRpcRequest{
		Jsonrpc: "2.0",
		Method:  MethodGetInfo,
		Params:  &GetInfoRequest{Token: token},
		ID:      requestID,
	}

	data, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	if err := h.transport.SendCustomMessage(ctx, lspPubkey, lsps0.LSPS_MESSAGE_TYPE_ID, data); err != nil {
		peerState.mu.Lock()
		delete(peerState.PendingGetInfoRequests, requestID)
		peerState.mu.Unlock()
		return "", fmt.Errorf("failed to send custom message: %w", err)
	}

	return requestID, nil
}

// SelectOpeningParams confirms selected opening parameters and requests invoice params
func (h *ClientHandler) SelectOpeningParams(ctx context.Context, lspPubkey string, paymentSizeMloki *uint64, openingFeeParams OpeningFeeParams) (string, error) {
	requestID := generateRequestID()

	h.mu.Lock()
	if _, ok := h.perPeerState[lspPubkey]; !ok {
		h.perPeerState[lspPubkey] = &PeerState{
			PendingGetInfoRequests: make(map[string]PendingRequest),
			PendingBuyRequests:     make(map[string]*InboundJITChannel),
		}
	}
	peerState := h.perPeerState[lspPubkey]
	h.mu.Unlock()

	peerState.mu.Lock()
	peerState.PendingBuyRequests[requestID] = &InboundJITChannel{
		PaymentSizeMloki: paymentSizeMloki,
		CreatedAt:        time.Now(),
	}
	peerState.mu.Unlock()

	request := &lsps0.JsonRpcRequest{
		Jsonrpc: "2.0",
		Method:  MethodBuy,
		Params: &BuyRequest{
			OpeningFeeParams: openingFeeParams,
			PaymentSizeMloki: paymentSizeMloki,
			PaymentSizeMsat:  paymentSizeMloki,
		},
		ID: requestID,
	}

	data, err := json.Marshal(request)
	if err != nil {
		peerState.mu.Lock()
		delete(peerState.PendingBuyRequests, requestID)
		peerState.mu.Unlock()
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Debug log
	logger.Logger.Info().Str("peer", lspPubkey).Msg("Sending BuyRequest")

	if err := h.transport.SendCustomMessage(ctx, lspPubkey, lsps0.LSPS_MESSAGE_TYPE_ID, data); err != nil {
		peerState.mu.Lock()
		delete(peerState.PendingBuyRequests, requestID)
		peerState.mu.Unlock()
		return "", fmt.Errorf("failed to send custom message: %w", err)
	}

	return requestID, nil
}

// HandleMessage processes incoming LSPS2 response messages
func (h *ClientHandler) HandleMessage(peerPubkey string, data []byte) error {
	var resp lsps0.JsonRpcResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err)
	}

	h.mu.RLock()
	peerState, ok := h.perPeerState[peerPubkey]
	h.mu.RUnlock()

	if !ok {
		return fmt.Errorf("received response from unknown peer: %s", peerPubkey)
	}

	// Check if it's a get_info response
	peerState.mu.Lock()
	_, isGetInfo := peerState.PendingGetInfoRequests[resp.ID]
	if isGetInfo {
		delete(peerState.PendingGetInfoRequests, resp.ID)
		peerState.mu.Unlock()
		return h.handleGetInfoResponse(peerPubkey, resp.ID, &resp)
	}

	// Check if it's a buy response
	jitChannel, isBuy := peerState.PendingBuyRequests[resp.ID]
	if isBuy {
		delete(peerState.PendingBuyRequests, resp.ID)
		peerState.mu.Unlock()
		return h.handleBuyResponse(peerPubkey, resp.ID, jitChannel, &resp)
	}
	peerState.mu.Unlock()

	return fmt.Errorf("received response for unknown request: %s", resp.ID)
}

func (h *ClientHandler) handleGetInfoResponse(peerPubkey, requestID string, resp *lsps0.JsonRpcResponse) error {
	if resp.Error != nil {
		h.eventQueue.Enqueue(&GetInfoFailedEvent{
			RequestID:          requestID,
			CounterpartyNodeID: peerPubkey,
			Error:              resp.Error.Message,
		})
		return nil
	}

	var result GetInfoResponse
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}
	// Debug log
	logger.Logger.Info().Str("peer", peerPubkey).Msg("Received GetInfo response")

	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return fmt.Errorf("failed to unmarshal get_info response: %w", err)
	}

	h.eventQueue.Enqueue(&OpeningParametersReadyEvent{
		RequestID:            requestID,
		CounterpartyNodeID:   peerPubkey,
		OpeningFeeParamsMenu: result.OpeningFeeParamsMenu,
	})

	return nil
}

func (h *ClientHandler) handleBuyResponse(peerPubkey, requestID string, jitChannel *InboundJITChannel, resp *lsps0.JsonRpcResponse) error {
	if resp.Error != nil {
		h.eventQueue.Enqueue(&BuyRequestFailedEvent{
			RequestID:          requestID,
			CounterpartyNodeID: peerPubkey,
			Error:              resp.Error.Message,
		})
		return nil
	}

	var result BuyResponse
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return fmt.Errorf("failed to unmarshal buy response: %w", err)
	}

	interceptSCID, err := result.ParseSCID()
	if err != nil {
		return fmt.Errorf("failed to parse intercept SCID: %w", err)
	}

	h.eventQueue.Enqueue(&InvoiceParametersReadyEvent{
		RequestID:          requestID,
		CounterpartyNodeID: peerPubkey,
		InterceptSCID:      interceptSCID,
		CLTVExpiryDelta:    result.LSPCLTVExpiryDelta,
		PaymentSizeMloki:   jitChannel.PaymentSizeMloki,
		LSPNodeID:          result.LSPNodeID,
	})

	return nil
}

// PrunePendingRequests removes requests older than the specified duration
func (h *ClientHandler) PrunePendingRequests(maxAge time.Duration) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	now := time.Now()

	for _, peerState := range h.perPeerState {
		peerState.mu.Lock()
		for id, req := range peerState.PendingGetInfoRequests {
			if now.Sub(req.CreatedAt) > maxAge {
				delete(peerState.PendingGetInfoRequests, id)
			}
		}
		for id, req := range peerState.PendingBuyRequests {
			if now.Sub(req.CreatedAt) > maxAge {
				delete(peerState.PendingBuyRequests, id)
			}
		}
		peerState.mu.Unlock()
	}
}
