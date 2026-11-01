package notifications

import (
	"context"
	"fmt"
)

// TransportType identifies the notification transport mechanism
type TransportType string

const (
	// TransportTypePush represents HTTPS webhook delivery
	TransportTypePush TransportType = "push"
	// TransportTypeNostr represents Nostr encrypted DM delivery
	TransportTypeNostr TransportType = "nostr"
)

// NotificationTransport abstracts the delivery mechanism for LSPS5 notifications.
// Implementations handle the specifics of each transport (HTTPS, Nostr, etc.)
type NotificationTransport interface {
	// Send delivers a notification to the target.
	// For push: target is a webhook URL
	// For nostr: target is the recipient's npub
	Send(ctx context.Context, target string, method string, params interface{}) error

	// Type returns the transport type identifier
	Type() TransportType
}

// TransportConfig holds configuration for notification transports
type TransportConfig struct {
	// HTTPS transport config
	NodePrivateKey interface{} // *btcec.PrivateKey for signing

	// Nostr transport config
	NostrPrivateKey string   // nsec... or hex private key
	NostrRelays     []string // Relay URLs for publishing
}

// Transport errors
var (
	ErrInvalidPrivateKey  = fmt.Errorf("invalid private key type")
	ErrWebhookFailed      = fmt.Errorf("webhook delivery failed")
	ErrNostrPublishFailed = fmt.Errorf("nostr publish failed")
	// ErrWebhookAddressNotAllowed: the webhook host resolved to an address
	// delivery refuses to connect to (private, loopback, link-local).
	ErrWebhookAddressNotAllowed = fmt.Errorf("webhook address not allowed")
)
