package lightning

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/crypto/ecdsa"
	"github.com/tv42/zbase32"
)

// Lsps5Notification represents a JSON-RPC 2.0 Notification for LSPS5 webhooks
type Lsps5Notification struct {
	JsonRpc string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

// Lsps5ExpirySoonParams contains parameters for the expiry_soon notification
type Lsps5ExpirySoonParams struct {
	Timeout int `json:"timeout"`
}

// Lsps5PaymentIncomingParams contains parameters for the payment_incoming notification
type Lsps5PaymentIncomingParams struct {
	PaymentHash string `json:"payment_hash"`
}

// CreateLsps5Notification creates a JSON-RPC 2.0 notification object as per bLIP-55
func CreateLsps5Notification(method string, params interface{}) ([]byte, error) {
	if params == nil {
		params = struct{}{}
	}

	notification := Lsps5Notification{
		JsonRpc: "2.0",
		Method:  method,
		Params:  params,
	}

	return json.Marshal(notification)
}

// SignLsps5Message signs a message per bLIP-55 specification.
// Message format: "LSPS5: DO NOT SIGN THIS MESSAGE MANUALLY: LSP: At ${timestamp} I notify ${body}"
// Returns a zbase32-encoded signature.
func SignLsps5Message(privateKey *btcec.PrivateKey, timestamp string, body []byte) (string, error) {
	// Construct the message to sign per bLIP-55 spec
	message := fmt.Sprintf("LSPS5: DO NOT SIGN THIS MESSAGE MANUALLY: LSP: At %s I notify %s", timestamp, string(body))

	// Sign using the Lightning Message format (double SHA256 with prefix)
	msg := append(SignedMsgPrefix, []byte(message)...)
	first := sha256.Sum256(msg)
	second := sha256.Sum256(first[:])

	// Sign the hash with the private key
	sig := ecdsa.SignCompact(privateKey, second[:], true)

	// Return as zbase32-encoded string
	return zbase32.EncodeToString(sig), nil
}

// LSPS5 notification method constants
const (
	Lsps5MethodWebhookRegistered          = "lsps5.webhook_registered"
	Lsps5MethodPaymentIncoming            = "lsps5.payment_incoming"
	Lsps5MethodExpirySoon                 = "lsps5.expiry_soon"
	Lsps5MethodOrderStateChanged          = "lsps5.order_state_changed"
	Lsps5MethodLiquidityManagementRequest = "lsps5.liquidity_management_request"
	Lsps5MethodOnionMessageIncoming       = "lsps5.onion_message_incoming"
	Lsps5MethodGoodbye                    = "lsps5.goodbye"
)
