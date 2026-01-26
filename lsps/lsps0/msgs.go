// Package lsps0 implements the LSPS0 transport layer for Lightning Service Providers
package lsps0

import (
	"encoding/json"
	"fmt"
)

// Lokichain LSPS message type ID as per spec
const LSPS_MESSAGE_TYPE_ID = 51610

// Method names for LSPS0
const (
	MethodListProtocols = "lsps0.list_protocols"
	MethodGetInfo       = "lsps0.get_info"
)

// JsonRpcRequest represents a JSON-RPC 2.0 request
type JsonRpcRequest struct {
	Jsonrpc string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      string      `json:"id"`
}

// JsonRpcResponse represents a JSON-RPC 2.0 response
type JsonRpcResponse struct {
	Jsonrpc string        `json:"jsonrpc"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *JsonRpcError `json:"error,omitempty"`
	ID      string        `json:"id"`
}

// JsonRpcError represents a JSON-RPC 2.0 error
type JsonRpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Error implements the error interface
func (e *JsonRpcError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// ListProtocolsRequest is the request for listing supported protocols
type ListProtocolsRequest struct{}

// ListProtocolsResponse contains the list of supported protocols
type ListProtocolsResponse struct {
	Protocols []int `json:"protocols"`
}

// GetInfoRequest represents a request to get LSPS0 info
type GetInfoRequest struct{}

// GetInfoResponse represents the response to lsps0.get_info
type GetInfoResponse struct {
	SupportedVersions       []int  `json:"supported_versions"`
	NotificationNostrPubkey string `json:"notification_nostr_pubkey"`
}

// EncodeJsonRpc encodes a JSON-RPC request or response
func EncodeJsonRpc(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

// DecodeJsonRpcRequest decodes a JSON-RPC request
func DecodeJsonRpcRequest(data []byte) (*JsonRpcRequest, error) {
	var req JsonRpcRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

// DecodeJsonRpcResponse decodes a JSON-RPC response
func DecodeJsonRpcResponse(data []byte) (*JsonRpcResponse, error) {
	var resp JsonRpcResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
