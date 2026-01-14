// Package lsps0 implements the LSPS0 service handler
package lsps0

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/transport"
)

// ServiceHandler handles LSPS0 service-side operations (LSP perspective)
type ServiceHandler struct {
	transport          transport.Transport
	eventQueue         *events.EventQueue
	supportedProtocols []int
	mu                 sync.RWMutex
}

// ServiceConfig holds configuration for the service handler
type ServiceConfig struct {
	SupportedProtocols []int
}

// NewServiceHandler creates a new LSPS0 service handler
func NewServiceHandler(transport transport.Transport, eventQueue *events.EventQueue, config *ServiceConfig) *ServiceHandler {
	if config == nil {
		config = &ServiceConfig{
			SupportedProtocols: []int{0, 1, 2}, // Default: LSPS0, LSPS1, LSPS2
		}
	}

	return &ServiceHandler{
		transport:          transport,
		eventQueue:         eventQueue,
		supportedProtocols: config.SupportedProtocols,
	}
}

// SetSupportedProtocols updates the list of supported protocols
func (h *ServiceHandler) SetSupportedProtocols(protocols []int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.supportedProtocols = protocols
}

// HandleMessage processes incoming LSPS0 requests from clients
func (h *ServiceHandler) HandleMessage(ctx context.Context, peerPubkey string, data []byte) error {
	req, err := DecodeJsonRpcRequest(data)
	if err != nil {
		return h.sendError(ctx, peerPubkey, "", -32700, "Parse error", err)
	}

	switch req.Method {
	case MethodListProtocols:
		return h.handleListProtocols(ctx, peerPubkey, req)
	default:
		return h.sendError(ctx, peerPubkey, req.ID, -32601, "Method not found", nil)
	}
}

// handleListProtocols handles the list_protocols request
func (h *ServiceHandler) handleListProtocols(ctx context.Context, peerPubkey string, req *JsonRpcRequest) error {
	h.mu.RLock()
	protocols := make([]int, len(h.supportedProtocols))
	copy(protocols, h.supportedProtocols)
	h.mu.RUnlock()

	response := &JsonRpcResponse{
		Jsonrpc: "2.0",
		Result: &ListProtocolsResponse{
			Protocols: protocols,
		},
		ID: req.ID,
	}

	return h.sendResponse(ctx, peerPubkey, response)
}

// sendResponse sends a JSON-RPC response to the peer
func (h *ServiceHandler) sendResponse(ctx context.Context, peerPubkey string, resp *JsonRpcResponse) error {
	data, err := EncodeJsonRpc(resp)
	if err != nil {
		return fmt.Errorf("failed to encode response: %w", err)
	}

	if err := h.transport.SendCustomMessage(ctx, peerPubkey, LSPS_MESSAGE_TYPE_ID, data); err != nil {
		return fmt.Errorf("failed to send response: %w", err)
	}

	return nil
}

// sendError sends a JSON-RPC error response
func (h *ServiceHandler) sendError(ctx context.Context, peerPubkey string, requestID string, code int, message string, data interface{}) error {
	rpcError := &JsonRpcError{
		Code:    code,
		Message: message,
		Data:    data,
	}

	response := &JsonRpcResponse{
		Jsonrpc: "2.0",
		Error:   rpcError,
		ID:      requestID,
	}

	responseData, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("failed to marshal error response: %w", err)
	}

	if err := h.transport.SendCustomMessage(ctx, peerPubkey, LSPS_MESSAGE_TYPE_ID, responseData); err != nil {
		return fmt.Errorf("failed to send error response: %w", err)
	}

	return nil
}
