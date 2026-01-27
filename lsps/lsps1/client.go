package lsps1

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/lsps0"
	"github.com/flokiorg/lokihub/lsps/transport"
)

// ClientHandler handles LSPS1 client-side operations
type ClientHandler struct {
	transport    transport.Transport
	eventQueue   *events.EventQueue
	perPeerState map[string]*PeerState
	mu           sync.RWMutex
}

type PeerState struct {
	PendingGetInfoRequests     map[string]bool
	PendingCreateOrderRequests map[string]bool
	PendingGetOrderRequests    map[string]bool
	mu                         sync.Mutex
}

func NewClientHandler(transport transport.Transport, eventQueue *events.EventQueue) *ClientHandler {
	return &ClientHandler{
		transport:    transport,
		eventQueue:   eventQueue,
		perPeerState: make(map[string]*PeerState),
	}
}

func generateRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (h *ClientHandler) ensurePeerState(lspPubkey string) *PeerState {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.perPeerState[lspPubkey]; !ok {
		h.perPeerState[lspPubkey] = &PeerState{
			PendingGetInfoRequests:     make(map[string]bool),
			PendingCreateOrderRequests: make(map[string]bool),
			PendingGetOrderRequests:    make(map[string]bool),
		}
	}
	return h.perPeerState[lspPubkey]
}

func (h *ClientHandler) RequestSupportedOptions(ctx context.Context, lspPubkey string) (string, error) {
	peerState := h.ensurePeerState(lspPubkey)
	requestID := generateRequestID()

	peerState.mu.Lock()
	peerState.PendingGetInfoRequests[requestID] = true
	peerState.mu.Unlock()

	request := &lsps0.JsonRpcRequest{
		Jsonrpc: "2.0",
		Method:  MethodGetInfo,
		Params:  &GetInfoRequest{},
		ID:      requestID,
	}

	if err := h.sendRequest(ctx, lspPubkey, request); err != nil {
		peerState.mu.Lock()
		delete(peerState.PendingGetInfoRequests, requestID)
		peerState.mu.Unlock()
		return "", err
	}

	return requestID, nil
}

func (h *ClientHandler) CreateOrder(ctx context.Context, lspPubkey string, order OrderParams, refundAddr *string) (string, error) {
	peerState := h.ensurePeerState(lspPubkey)
	requestID := generateRequestID()

	peerState.mu.Lock()
	peerState.PendingCreateOrderRequests[requestID] = true
	peerState.mu.Unlock()

	request := &lsps0.JsonRpcRequest{
		Jsonrpc: "2.0",
		Method:  MethodCreateOrder,
		Params: &CreateOrderRequest{
			OrderParams:          order,
			RefundOnchainAddress: refundAddr,
		},
		ID: requestID,
	}

	if err := h.sendRequest(ctx, lspPubkey, request); err != nil {
		peerState.mu.Lock()
		delete(peerState.PendingCreateOrderRequests, requestID)
		peerState.mu.Unlock()
		return "", err
	}

	return requestID, nil
}

func (h *ClientHandler) CheckOrderStatus(ctx context.Context, lspPubkey string, orderID string) (string, error) {
	peerState := h.ensurePeerState(lspPubkey)
	requestID := generateRequestID()

	peerState.mu.Lock()
	peerState.PendingGetOrderRequests[requestID] = true
	peerState.mu.Unlock()

	request := &lsps0.JsonRpcRequest{
		Jsonrpc: "2.0",
		Method:  MethodGetOrder,
		Params:  &GetOrderRequest{OrderID: orderID},
		ID:      requestID,
	}

	if err := h.sendRequest(ctx, lspPubkey, request); err != nil {
		peerState.mu.Lock()
		delete(peerState.PendingGetOrderRequests, requestID)
		peerState.mu.Unlock()
		return "", err
	}

	return requestID, nil
}

func (h *ClientHandler) sendRequest(ctx context.Context, peerPubkey string, request *lsps0.JsonRpcRequest) error {
	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	if err := h.transport.SendCustomMessage(ctx, peerPubkey, lsps0.LSPS_MESSAGE_TYPE_ID, data); err != nil {
		return fmt.Errorf("failed to send custom message: %w", err)
	}
	return nil
}

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

	peerState.mu.Lock()
	defer peerState.mu.Unlock()

	if _, ok := peerState.PendingGetInfoRequests[resp.ID]; ok {
		delete(peerState.PendingGetInfoRequests, resp.ID)
		return h.handleGetInfoResponse(peerPubkey, resp.ID, &resp)
	}

	if _, ok := peerState.PendingCreateOrderRequests[resp.ID]; ok {
		delete(peerState.PendingCreateOrderRequests, resp.ID)
		return h.handleCreateOrderResponse(peerPubkey, resp.ID, &resp)
	}

	if _, ok := peerState.PendingGetOrderRequests[resp.ID]; ok {
		delete(peerState.PendingGetOrderRequests, resp.ID)
		return h.handleGetOrderResponse(peerPubkey, resp.ID, &resp)
	}

	return fmt.Errorf("received response for unknown request: %s", resp.ID)
}

func (h *ClientHandler) handleGetInfoResponse(peerPubkey, requestID string, resp *lsps0.JsonRpcResponse) error {
	if resp.Error != nil {
		h.eventQueue.Enqueue(&SupportedOptionsFailedEvent{
			RequestID:          requestID,
			CounterpartyNodeID: peerPubkey,
			Error:              resp.Error.Message,
			ErrorCode:          resp.Error.Code,
			ErrorData:          safeCastErrorData(resp.Error.Data),
		})
		return nil
	}

	var result GetInfoResponse
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return err
	}

	h.eventQueue.Enqueue(&SupportedOptionsReadyEvent{
		RequestID:          requestID,
		CounterpartyNodeID: peerPubkey,
		SupportedOptions:   result.Options,
	})
	return nil
}

func (h *ClientHandler) handleCreateOrderResponse(peerPubkey, requestID string, resp *lsps0.JsonRpcResponse) error {
	if resp.Error != nil {
		// Specific error handling
		switch resp.Error.Code {
		case 1: // invalid_params
			// We could enrich the error message here
			resp.Error.Message = fmt.Sprintf("Invalid parameters: %s", resp.Error.Message)
		case 2: // option_mismatch
			resp.Error.Message = fmt.Sprintf("Option mismatch: %s", resp.Error.Message)
		}

		h.eventQueue.Enqueue(&OrderRequestFailedEvent{
			RequestID:          requestID,
			CounterpartyNodeID: peerPubkey,
			Error:              resp.Error.Message,
			ErrorCode:          resp.Error.Code,
			ErrorData:          safeCastErrorData(resp.Error.Data),
		})
		return nil
	}

	var result CreateOrderResponse
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return err
	}

	h.eventQueue.Enqueue(&OrderCreatedEvent{
		RequestID:          requestID,
		CounterpartyNodeID: peerPubkey,
		OrderID:            result.OrderID,
		Order:              result.OrderParams,
		OrderState:         result.OrderState, // Populating new field
		Payment:            result.Payment,
		Channel:            result.Channel,
	})
	return nil
}

func (h *ClientHandler) handleGetOrderResponse(peerPubkey, requestID string, resp *lsps0.JsonRpcResponse) error {
	if resp.Error != nil {
		h.eventQueue.Enqueue(&OrderRequestFailedEvent{
			RequestID:          requestID,
			CounterpartyNodeID: peerPubkey,
			Error:              resp.Error.Message,
			ErrorCode:          resp.Error.Code,
			ErrorData:          safeCastErrorData(resp.Error.Data),
		})
		return nil
	}

	var result CreateOrderResponse
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return err
	}

	h.eventQueue.Enqueue(&OrderStatusEvent{
		RequestID:          requestID,
		CounterpartyNodeID: peerPubkey,
		OrderID:            result.OrderID,
		Order:              result.OrderParams,
		OrderState:         result.OrderState, // Populating new field
		Payment:            result.Payment,
		Channel:            result.Channel,
	})
	return nil
}

func safeCastErrorData(data interface{}) map[string]interface{} {
	if data == nil {
		return nil
	}
	if m, ok := data.(map[string]interface{}); ok {
		return m
	}
	return nil
}
