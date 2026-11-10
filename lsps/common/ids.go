package common

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// NewRequestID returns a random JSON-RPC request id shared by every LSPS client.
func NewRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand only fails if the OS entropy source is unavailable;
		// fall back to a timestamp-derived ID rather than blocking the request.
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// NodeKeyOf is the x-only node key of a node key or a 66-hex node id.
func NodeKeyOf(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if len(key) == 66 && (strings.HasPrefix(key, "02") || strings.HasPrefix(key, "03")) {
		return key[2:]
	}
	return key
}
