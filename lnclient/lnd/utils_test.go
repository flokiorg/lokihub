package lnd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatConnectionAddress(t *testing.T) {
	tests := []struct {
		name     string
		address  string
		port     uint16
		expected string
	}{
		{
			name:     "Address with port 0",
			address:  "127.0.0.1:8080",
			port:     0,
			expected: "127.0.0.1:8080",
		},
		{
			name:     "Address with non-zero port",
			address:  "127.0.0.1",
			port:     5521,
			expected: "127.0.0.1:5521",
		},
		{
			name:     "Address without port, port 0 (edge case, likely invalid but function should behave)",
			address:  "127.0.0.1",
			port:     0,
			expected: "127.0.0.1",
		},
		{
			name:     "Hostname with port 0",
			address:  "node.loki:10009",
			port:     0,
			expected: "node.loki:10009",
		},
		{
			name:     "Hostname with non-zero port",
			address:  "node.loki",
			port:     10009,
			expected: "node.loki:10009",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatConnectionAddress(tt.address, tt.port)
			assert.Equal(t, tt.expected, result)
		})
	}
}
