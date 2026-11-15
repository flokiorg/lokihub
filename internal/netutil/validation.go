package netutil

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
