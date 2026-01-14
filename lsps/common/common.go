package common

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Amount represents satoshis, serialized as a string in JSON
type Amount uint64

func (a Amount) MarshalJSON() ([]byte, error) {
	return json.Marshal(fmt.Sprintf("%d", a))
}

func (a *Amount) UnmarshalJSON(data []byte) error {
	var s string
	// Try parsing as string first
	if err := json.Unmarshal(data, &s); err == nil {
		val, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		*a = Amount(val)
		return nil
	}

	// Fallback: try parsing as number (for flexibility, though spec says string)
	var n uint64
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*a = Amount(n)
	return nil
}

// UnixTime represents a timestamp
type UnixTime int64

// JsonRpcRequest represents a JSON-RPC 2.0 request
type JsonRpcRequest struct {
	Jsonrpc string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      string          `json:"id"`
}

// JsonRpcResponse represents a JSON-RPC 2.0 response
type JsonRpcResponse struct {
	Jsonrpc string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JsonRpcError   `json:"error,omitempty"`
	ID      string          `json:"id"`
}

type JsonRpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}
