package lsps5

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"

	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/lsps0"
	"github.com/flokiorg/lokihub/lsps/transport"
)

// ClientHandler handles LSPS5 client-side operations
type ClientHandler struct {
	transport    transport.Transport
	eventQueue   *events.EventQueue
	perPeerState map[string]*PeerState
	mu           sync.RWMutex
}

type SetWebhookReqInfo struct {
	AppName    string
	WebhookURL string
}

type RemoveWebhookReqInfo struct {
	AppName string
}

type PeerState struct {
	PendingSetWebhookRequests    map[string]SetWebhookReqInfo
	PendingListWebhooksRequests  map[string]bool
	PendingRemoveWebhookRequests map[string]RemoveWebhookReqInfo
	mu                           sync.Mutex
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
			PendingSetWebhookRequests:    make(map[string]SetWebhookReqInfo),
			PendingListWebhooksRequests:  make(map[string]bool),
			PendingRemoveWebhookRequests: make(map[string]RemoveWebhookReqInfo),
		}
	}
	return h.perPeerState[lspPubkey]
}

func (h *ClientHandler) SetWebhook(ctx context.Context, lspPubkey string, appName, webhookURL, transport string) (string, error) {
	if err := validateWebhookURL(webhookURL); err != nil {
		return "", err
	}

	peerState := h.ensurePeerState(lspPubkey)
	requestID := generateRequestID()

	peerState.mu.Lock()
	peerState.PendingSetWebhookRequests[requestID] = SetWebhookReqInfo{
		AppName:    appName,
		WebhookURL: webhookURL,
	}
	peerState.mu.Unlock()

	request := &lsps0.JsonRpcRequest{
		Jsonrpc: "2.0",
		Method:  MethodSetWebhook,
		Params: &SetWebhookRequest{
			AppName:   appName,
			Webhook:   webhookURL,
			Transport: transport,
		},
		ID: requestID,
	}

	if err := h.sendRequest(ctx, lspPubkey, request); err != nil {
		peerState.mu.Lock()
		delete(peerState.PendingSetWebhookRequests, requestID)
		peerState.mu.Unlock()
		return "", err
	}

	return requestID, nil
}

func (h *ClientHandler) ListWebhooks(ctx context.Context, lspPubkey string) (string, error) {
	peerState := h.ensurePeerState(lspPubkey)
	requestID := generateRequestID()

	peerState.mu.Lock()
	peerState.PendingListWebhooksRequests[requestID] = true
	peerState.mu.Unlock()

	request := &lsps0.JsonRpcRequest{
		Jsonrpc: "2.0",
		Method:  MethodListWebhooks,
		Params:  &ListWebhooksRequest{},
		ID:      requestID,
	}

	if err := h.sendRequest(ctx, lspPubkey, request); err != nil {
		peerState.mu.Lock()
		delete(peerState.PendingListWebhooksRequests, requestID)
		peerState.mu.Unlock()
		return "", err
	}

	return requestID, nil
}

func (h *ClientHandler) RemoveWebhook(ctx context.Context, lspPubkey string, appName string) (string, error) {
	peerState := h.ensurePeerState(lspPubkey)
	requestID := generateRequestID()

	peerState.mu.Lock()
	peerState.PendingRemoveWebhookRequests[requestID] = RemoveWebhookReqInfo{
		AppName: appName,
	}
	peerState.mu.Unlock()

	request := &lsps0.JsonRpcRequest{
		Jsonrpc: "2.0",
		Method:  MethodRemoveWebhook,
		Params: &RemoveWebhookRequest{
			AppName: appName,
		},
		ID: requestID,
	}

	if err := h.sendRequest(ctx, lspPubkey, request); err != nil {
		peerState.mu.Lock()
		delete(peerState.PendingRemoveWebhookRequests, requestID)
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

	if reqInfo, ok := peerState.PendingSetWebhookRequests[resp.ID]; ok {
		delete(peerState.PendingSetWebhookRequests, resp.ID)
		return h.handleSetWebhookResponse(peerPubkey, resp.ID, reqInfo, &resp)
	}

	if _, ok := peerState.PendingListWebhooksRequests[resp.ID]; ok {
		delete(peerState.PendingListWebhooksRequests, resp.ID)
		return h.handleListWebhooksResponse(peerPubkey, resp.ID, &resp)
	}

	if reqInfo, ok := peerState.PendingRemoveWebhookRequests[resp.ID]; ok {
		delete(peerState.PendingRemoveWebhookRequests, resp.ID)
		return h.handleRemoveWebhookResponse(peerPubkey, resp.ID, reqInfo, &resp)
	}

	return fmt.Errorf("received response for unknown request: %s", resp.ID)
}

func (h *ClientHandler) handleSetWebhookResponse(peerPubkey, requestID string, reqInfo SetWebhookReqInfo, resp *lsps0.JsonRpcResponse) error {
	if resp.Error != nil {
		h.eventQueue.Enqueue(&WebhookRegistrationFailedEvent{
			RequestID:          requestID,
			CounterpartyNodeID: peerPubkey,
			AppName:            reqInfo.AppName,
			WebhookURL:         reqInfo.WebhookURL,
			Error:              resp.Error.Message,
		})
		return nil
	}

	var result SetWebhookResponse
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return err
	}

	h.eventQueue.Enqueue(&WebhookRegisteredEvent{
		RequestID:          requestID,
		CounterpartyNodeID: peerPubkey,
		AppName:            reqInfo.AppName,
		WebhookURL:         reqInfo.WebhookURL,
		NumWebhooks:        result.NumWebhooks,
		MaxWebhooks:        result.MaxWebhooks,
		NoChange:           result.NoChange,
	})
	return nil
}

func (h *ClientHandler) handleListWebhooksResponse(peerPubkey, requestID string, resp *lsps0.JsonRpcResponse) error {
	if resp.Error != nil {
		return fmt.Errorf("list webhooks failed: %s", resp.Error.Message)
	}

	var result ListWebhooksResponse
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return err
	}

	h.eventQueue.Enqueue(&WebhooksListedEvent{
		RequestID:          requestID,
		CounterpartyNodeID: peerPubkey,
		AppNames:           result.AppNames,
		MaxWebhooks:        result.MaxWebhooks,
	})
	return nil
}

func (h *ClientHandler) handleRemoveWebhookResponse(peerPubkey, requestID string, reqInfo RemoveWebhookReqInfo, resp *lsps0.JsonRpcResponse) error {
	if resp.Error != nil {
		h.eventQueue.Enqueue(&WebhookRemovalFailedEvent{
			RequestID:          requestID,
			CounterpartyNodeID: peerPubkey,
			AppName:            reqInfo.AppName,
			Error:              resp.Error.Message,
		})
		return nil
	}

	h.eventQueue.Enqueue(&WebhookRemovedEvent{
		RequestID:          requestID,
		CounterpartyNodeID: peerPubkey,
		AppName:            reqInfo.AppName,
	})
	return nil
}

func validateWebhookURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("webhook URL must use http or https scheme")
	}
	if parsed.Host == "" {
		return fmt.Errorf("webhook URL must have a host")
	}
	return nil
}
