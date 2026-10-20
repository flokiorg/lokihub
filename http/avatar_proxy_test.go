package http

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The fetch behind both the web and desktop avatar routes refuses anything
// but a public http(s) host, so a profile picture URL can't make the backend
// probe the user's own machine or LAN.
func TestFetchAvatar_RefusesNonPublicOrNonHTTP(t *testing.T) {
	for rawURL, want := range map[string]error{
		"ftp://example.com/a.png":           ErrAvatarBadURL,
		"not a url":                         ErrAvatarBadURL,
		"http://127.0.0.1/a.png":            ErrAvatarHostNotAllowed,
		"http://localhost/a.png":            ErrAvatarHostNotAllowed,
		"http://10.0.0.1/a.png":             ErrAvatarHostNotAllowed,
		"http://169.254.169.254/latest":     ErrAvatarHostNotAllowed,
		"http://[::1]/a.png":                ErrAvatarHostNotAllowed,
		"https://192.168.1.1:8443/pic.webp": ErrAvatarHostNotAllowed,
	} {
		_, err := FetchAvatar(context.Background(), rawURL)
		assert.ErrorIs(t, err, want, rawURL)
	}
}
