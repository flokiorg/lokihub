package utils

import (
	"fmt"
	"net/url"
)

func ValidateWebSocketURL(urlStr string) error {
	u, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "wss" && u.Scheme != "ws" {
		return fmt.Errorf("URL must start with wss:// or ws://")
	}
	if u.Host == "" {
		return fmt.Errorf("URL must have a host")
	}
	return nil
}

func ValidateHTTPURL(urlStr string) error {
	u, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("URL must start with https:// or http://")
	}
	if u.Host == "" {
		return fmt.Errorf("URL must have a host")
	}
	return nil
}

func ValidateMessageBoardURL(urlStr string) error {
	u, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if u.Scheme != "nostr+walletconnect" {
		return fmt.Errorf("schema must be nostr+walletconnect://")
	}

	// Host should be the pubkey (64 hex chars)
	if len(u.Host) != 64 {
		return fmt.Errorf("invalid pubkey: length must be 64 characters")
	}
	// Verify it is hex
	for _, c := range u.Host {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return fmt.Errorf("invalid pubkey: must be hex")
		}
	}

	q := u.Query()
	if q.Get("relay") == "" {
		return fmt.Errorf("missing relay parameter")
	}
	if q.Get("secret") == "" {
		return fmt.Errorf("missing secret parameter")
	}

	return nil
}
