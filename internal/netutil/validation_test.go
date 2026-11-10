package netutil

import (
	"testing"
)

func TestValidateWebSocketURL(t *testing.T) {
	tests := []struct {
		urlStr  string
		wantErr bool
	}{
		{"wss://relay.damus.io", false},
		{"ws://relay.damus.io", false},
		{"https://relay.damus.io", true},
		{"invalid-url", true},
		{"wss://", true},
	}
	for _, tt := range tests {
		t.Run(tt.urlStr, func(t *testing.T) {
			if err := ValidateWebSocketURL(tt.urlStr); (err != nil) != tt.wantErr {
				t.Errorf("ValidateWebSocketURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateHTTPURL(t *testing.T) {
	tests := []struct {
		urlStr  string
		wantErr bool
	}{
		{"https://example.com", false},
		{"http://example.com", false},
		{"ftp://example.com", true},
		{"invalid-url", true},
		{"https://", true},
	}
	for _, tt := range tests {
		t.Run(tt.urlStr, func(t *testing.T) {
			if err := ValidateHTTPURL(tt.urlStr); (err != nil) != tt.wantErr {
				t.Errorf("ValidateHTTPURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
