package common

import (
	"fmt"
	"net"
	"net/url"
)

// ValidateRelayUrl applies the webhook SSRF address policy to a Nostr relay
// URL (ws:// or wss://). The LSPS5 nostr transport dials client-supplied
// relays, so -- like the push webhook branch's common.ValidateWebhookUrl -- a
// relay whose host resolves to a private/loopback/link-local/CGNAT address
// must be refused, both at set_webhook registration and again before the
// transport dials it (FLSPD-0011 / R-LSP-6).
//
// allowLoopback admits loopback relays for local development and tests
// (LSPS5_ALLOW_LOOPBACK_WEBHOOKS); every other non-public range stays
// refused. The hostname is resolved here and must be re-checked at dial time,
// since DNS can change between registration and delivery.
func ValidateRelayUrl(rawUrl string, allowLoopback bool) error {
	u, err := url.Parse(rawUrl)
	if err != nil {
		return fmt.Errorf("invalid relay url format: %w", err)
	}

	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("unsupported_protocol: relay scheme must be ws or wss")
	}

	hostname := u.Hostname()
	if hostname == "" {
		return fmt.Errorf("relay url must have a hostname")
	}

	ips, err := net.LookupIP(hostname)
	if err != nil {
		return fmt.Errorf("failed to lookup relay hostname: %w", err)
	}

	for _, ip := range ips {
		if allowLoopback && ip.IsLoopback() {
			continue
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("relay url resolves to private ip: %s", ip.String())
		}
	}

	return nil
}
