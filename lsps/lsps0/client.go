// Package lsps0 implements the LSPS0 client handler
package lsps0

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/transport"
	"github.com/google/uuid"
)

// ClientHandler handles LSPS0 client-side operations
type ClientHandler struct {
	transport   transport.Transport
	eventQueue  *events.EventQueue
	pendingReqs map[string]chan *JsonRpcResponse
	mu          sync.RWMutex
}

// NewClientHandler creates a new LSPS0 client handler
func NewClientHandler(transport transport.Transport, eventQueue *events.EventQueue) *ClientHandler {
	return &ClientHandler{
		transport:   transport,
		eventQueue:  eventQueue,
		pendingReqs: make(map[string]chan *JsonRpcResponse),
	}
}

// ListProtocols requests the list of supported protocols from the LSP
func (h *ClientHandler) ListProtocols(ctx context.Context, peerPubkey string) ([]int, error) {
	requestID := uuid.New().String()

	req := &JsonRpcRequest{
		Jsonrpc: "2.0",
		Method:  MethodListProtocols,
		Params:  &ListProtocolsRequest{},
		ID:      requestID,
	}

	data, err := EncodeJsonRpc(req)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	// Create response channel
	responseChan := make(chan *JsonRpcResponse, 1)
	h.mu.Lock()
	h.pendingReqs[requestID] = responseChan
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.pendingReqs, requestID)
		h.mu.Unlock()
	}()

	// Send message
	if err := h.transport.SendCustomMessage(ctx, peerPubkey, LSPS_MESSAGE_TYPE_ID, data); err != nil {
		return nil, fmt.Errorf("failed to send custom message: %w", err)
	}

	// Wait for response
	select {
	case resp := <-responseChan:
		if resp.Error != nil {
			return nil, resp.Error
		}

		// Parse result
		var result ListProtocolsResponse
		resultBytes, err := json.Marshal(resp.Result)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal result: %w", err)
		}
		if err := json.Unmarshal(resultBytes, &result); err != nil {
			return nil, fmt.Errorf("failed to unmarshal result: %w", err)
		}

		return result.Protocols, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// GetInfo requests general info from the LSP
func (h *ClientHandler) GetInfo(ctx context.Context, peerPubkey string) (*GetInfoResponse, error) {
	requestID := uuid.New().String()

	req := &JsonRpcRequest{
		Jsonrpc: "2.0",
		Method:  MethodGetInfo,
		Params:  &GetInfoRequest{},
		ID:      requestID,
	}

	data, err := EncodeJsonRpc(req)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	// Create response channel
	responseChan := make(chan *JsonRpcResponse, 1)
	h.mu.Lock()
	h.pendingReqs[requestID] = responseChan
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.pendingReqs, requestID)
		h.mu.Unlock()
	}()

	// Send message
	if err := h.transport.SendCustomMessage(ctx, peerPubkey, LSPS_MESSAGE_TYPE_ID, data); err != nil {
		return nil, fmt.Errorf("failed to send custom message: %w", err)
	}

	// Wait for response
	select {
	case resp := <-responseChan:
		if resp.Error != nil {
			return nil, resp.Error
		}

		// Parse result
		var result GetInfoResponse
		resultBytes, err := json.Marshal(resp.Result)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal result: %w", err)
		}
		if err := json.Unmarshal(resultBytes, &result); err != nil {
			return nil, fmt.Errorf("failed to unmarshal result: %w", err)
		}

		return &result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// HandleMessage processes incoming LSPS0 messages
func (h *ClientHandler) HandleMessage(peerPubkey string, data []byte) error {
	resp, err := DecodeJsonRpcResponse(data)
	if err != nil {
		return fmt.Errorf("failed to decode JSON-RPC response: %w", err)
	}

	h.mu.RLock()
	responseChan, ok := h.pendingReqs[resp.ID]
	h.mu.RUnlock()

	if ok {
		select {
		case responseChan <- resp:
		default:
			// Channel full or closed
		}
		return nil
	}

	return fmt.Errorf("received response for unknown request: %s", resp.ID)
}
