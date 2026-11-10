// Package netutil validates and parses network addresses and URLs.
package netutil

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ParseHostPort parses a host string which may or may not contain a port.
// If the port is missing, it returns the host and 0 as port.
// If the port is present, it parses it.
// It handles "host:port" and "host" cases.
func ParseHostPort(input string) (host string, port uint16, err error) {
	if input == "" {
		return "", 0, fmt.Errorf("host cannot be empty")
	}

	// Try splitting host and port
	h, pStr, err := net.SplitHostPort(input)
	if err != nil {
		// net.SplitHostPort returns an error if missing port (e.g. "missing port in address")
		// or if there are too many colons (IPv6 without brackets, etc, though we assume typical hostname/IP)
		// If error contains "missing port", we treat it as host only.
		if strings.Contains(err.Error(), "missing port") {
			return input, 0, nil
		}
		return "", 0, err
	}

	p, err := strconv.Atoi(pStr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port: %w", err)
	}

	if p < 0 || p > 65535 {
		return "", 0, fmt.Errorf("invalid port number: %d", p)
	}

	return h, uint16(p), nil
}
