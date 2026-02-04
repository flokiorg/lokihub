package utils

import (
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

func ReadFileTail(filePath string, maxLen int) (data []byte, err error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer func() {
		err = f.Close()
		if err != nil {
			err = fmt.Errorf("failed to close file: %w", err)
			data = nil
		}
	}()

	var dataReader io.Reader = f

	if maxLen > 0 {
		stat, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("failed to stat file: %w", err)
		}

		if stat.Size() > int64(maxLen) {
			_, err = f.Seek(-int64(maxLen), io.SeekEnd)
			if err != nil {
				return nil, fmt.Errorf("failed to seek file: %w", err)
			}
		}

		dataReader = io.LimitReader(f, int64(maxLen))
	}

	data, err = io.ReadAll(dataReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return data, nil
}

// filters values from a slice
func Filter[T any](s []T, f func(T) bool) []T {
	var r []T
	for _, v := range s {
		if f(v) {
			r = append(r, v)
		}
	}
	return r
}

func ParseCommandLine(s string) ([]string, error) {
	args := make([]string, 0)
	var currentArg strings.Builder
	inQuotes := false
	escaped := false

	for _, r := range s {
		switch {
		case escaped:
			currentArg.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			inQuotes = !inQuotes
		case unicode.IsSpace(r) && !inQuotes:
			if currentArg.Len() > 0 {
				args = append(args, currentArg.String())
				currentArg.Reset()
			}
		default:
			currentArg.WriteRune(r)
		}
	}

	if escaped || inQuotes {
		return nil, fmt.Errorf("unexpected end of string")
	}

	if currentArg.Len() > 0 {
		args = append(args, currentArg.String())
	}

	return args, nil
}

var pubkeyRegex = regexp.MustCompile(`^[0-9a-fA-F]{66}$`)

// ParseLSPURI parses an LSP URI in the format pubkey@host:port
func ParseLSPURI(uri string) (pubkey, host string, err error) {
	parts := strings.Split(uri, "@")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid URI format: expected pubkey@host:port")
	}
	pubkey = strings.ToLower(parts[0])
	host = parts[1]

	if !pubkeyRegex.MatchString(pubkey) {
		return "", "", fmt.Errorf("invalid pubkey format: expected 33-byte hex")
	}
	if host == "" {
		return "", "", fmt.Errorf("host cannot be empty")
	}

	return pubkey, host, nil
}

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
