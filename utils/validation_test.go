package utils

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

func TestValidateMessageBoardURL(t *testing.T) {
	validPubkey := "b42337d4576dfd22384a654eac18840c558c4456543b573663a702952865955a"
	validSecret := "f9d86337d4576dfd22384a654eac18840c558c4456543b573663a702952865955a"
	tests := []struct {
		name    string
		urlStr  string
		wantErr bool
	}{
		{
			"valid",
			"nostr+walletconnect://" + validPubkey + "?relay=wss://relay.damus.io&secret=" + validSecret,
			false,
		},
		{
			"wrong scheme",
			"nostr://" + validPubkey + "?relay=wss://relay.damus.io&secret=" + validSecret,
			true,
		},
		{
			"invalid pubkey length",
			"nostr+walletconnect://123?relay=wss://relay.damus.io&secret=" + validSecret,
			true,
		},
		{
			"invalid pubkey chars",
			"nostr+walletconnect://" + "z" + validPubkey[1:] + "?relay=wss://relay.damus.io&secret=" + validSecret,
			true,
		},
		{
			"missing relay",
			"nostr+walletconnect://" + validPubkey + "?secret=" + validSecret,
			true,
		},
		{
			"missing secret",
			"nostr+walletconnect://" + validPubkey + "?relay=wss://relay.damus.io",
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateMessageBoardURL(tt.urlStr); (err != nil) != tt.wantErr {
				t.Errorf("ValidateMessageBoardURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
