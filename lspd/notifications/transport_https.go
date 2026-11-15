package notifications

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/lokihub/lspd/common"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/platform/logging"
	"github.com/rs/zerolog/log"
)

// webhookTimeout bounds one whole webhook delivery (connect, TLS, request,
// response headers and body).
const webhookTimeout = 30 * time.Second

// HTTPSTransport implements NotificationTransport for HTTPS webhook delivery.
// It sends bLIP-55 compliant JSON-RPC 2.0 notifications with signed headers.
type HTTPSTransport struct {
	nodePrivKey interface{} // *btcec.PrivateKey
	client      *http.Client
}

// NewHTTPSTransport creates a new HTTPS webhook transport whose client
// refuses to connect to private or loopback addresses and does not follow
// redirects.
func NewHTTPSTransport(nodePrivKey interface{}) *HTTPSTransport {
	return &HTTPSTransport{
		nodePrivKey: nodePrivKey,
		client:      newWebhookClient(false),
	}
}

// newWebhookClient builds the client used for webhook delivery. Webhook URLs
// are client-supplied and only validated at registration; the hostname is
// resolved again here, so the address policy is enforced on the actual IP
// being dialled. Redirects are not followed (a 3xx is a failed delivery).
// allowLoopback admits loopback addresses for local development and tests
// (LSPS5_ALLOW_LOOPBACK_WEBHOOKS); other private ranges stay refused.
func newWebhookClient(allowLoopback bool) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   webhookDialControl(allowLoopback),
	}
	transport := &http.Transport{
		// No proxy: a proxy would dial the target on our behalf and bypass
		// the address check below.
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   webhookTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// webhookDialControl runs after DNS resolution, immediately before each
// connect, with the literal IP being dialled.
func webhookDialControl(allowLoopback bool) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrWebhookAddressNotAllowed, err)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("%w: %s", ErrWebhookAddressNotAllowed, host)
		}
		if allowLoopback && ip.IsLoopback() {
			return nil
		}
		if common.IsPrivateIP(ip) {
			return fmt.Errorf("%w: %s", ErrWebhookAddressNotAllowed, ip)
		}
		return nil
	}
}

// Type returns TransportTypePush
func (t *HTTPSTransport) Type() TransportType {
	return TransportTypePush
}

// Send delivers a notification via HTTPS POST to the webhook URL.
// It creates a JSON-RPC 2.0 notification, signs it per bLIP-55, and includes
// the required x-lsps5-timestamp and x-lsps5-signature headers.
func (t *HTTPSTransport) Send(ctx context.Context, webhookURL string, method string, params interface{}) error {
	// Type assert nodePrivKey
	privKey, ok := t.nodePrivKey.(*btcec.PrivateKey)
	if !ok {
		log.Error().
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionFailure)).
			Msgf("HTTPSTransport: invalid private key type: %T", t.nodePrivKey)
		return ErrInvalidPrivateKey
	}

	// Create JSON-RPC 2.0 notification body
	body, err := lightning.CreateLsps5Notification(method, params)
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionFailure)).
			Str("method", method).
			Msg("HTTPSTransport: Failed to create LSPS5 notification body")
		return err
	}

	// Generate ISO8601 timestamp per bLIP-55: YYYY-MM-DDThh:mm:ss.uuuZ
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	log.Trace().
		Str("component", string(logging.LogComponentNotifications)).
		Str("action", "signing").
		Str("timestamp", timestamp).
		Msg("HTTPSTransport: signing message")

	// Sign the message per bLIP-55 specification
	signature, err := lightning.SignLsps5Message(privKey, timestamp, body)
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionFailure)).
			Str("method", method).
			Msg("HTTPSTransport: Failed to sign LSPS5 notification")
		return err
	}

	// Create HTTP POST request with required headers
	req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(body))
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionFailure)).
			Str("url", webhookURL).
			Msg("HTTPSTransport: Failed to create HTTP request")
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-lsps5-timestamp", timestamp)
	req.Header.Set("x-lsps5-signature", signature)

	log.Trace().
		Str("component", string(logging.LogComponentNotifications)).
		Str("action", "sending_http").
		Str("url", webhookURL).
		Str("signature", signature).
		Msg("HTTPSTransport: sending request")

	resp, err := t.client.Do(req)
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionFailure)).
			Str("url", webhookURL).
			Str("method", method).
			Msg("HTTPSTransport: Webhook delivery failed (network error)")
		return err
	}
	defer resp.Body.Close()

	// Per bLIP-55: only 200 OK is considered success
	if resp.StatusCode != http.StatusOK {
		buf := make([]byte, 1000)
		bytesRead, _ := io.ReadFull(resp.Body, buf)
		respBody := buf[:bytesRead]
		log.Error().
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionFailure)).
			Str("url", webhookURL).
			Str("method", method).
			Str("status", resp.Status).
			Str("body", string(respBody)).
			Msg("HTTPSTransport: Webhook returned non-200 status")
		return ErrWebhookFailed
	}

	log.Info().
		Str("component", string(logging.LogComponentNotifications)).
		Str("action", string(logging.LogActionSuccess)).
		Str("url", webhookURL).
		Str("method", method).
		Msg("HTTPS: notification delivered")

	return nil
}
