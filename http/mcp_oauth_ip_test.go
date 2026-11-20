package http

import (
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// TestOAuthClientIP (re-audit N2): behind the trusted local proxy the limiter
// keys on the proxy-appended last XFF hop; a direct public peer cannot spoof it.
func TestOAuthClientIP(t *testing.T) {
	e := echo.New()
	cases := []struct{ remote, xff, want string }{
		{"172.17.0.5:1234", "1.2.3.4", "1.2.3.4"},          // via proxy
		{"172.17.0.5:1234", "9.9.9.9, 1.2.3.4", "1.2.3.4"}, // client-forged first hop ignored
		{"127.0.0.1:1234", "", "127.0.0.1"},                // no proxy header
		{"8.8.8.8:1234", "1.2.3.4", "8.8.8.8"},             // public peer: XFF untrusted
		{"172.17.0.5:1234", "not-an-ip", "172.17.0.5"},     // garbage hop
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", "/oauth/register", nil)
		req.RemoteAddr = c.remote
		if c.xff != "" {
			req.Header.Set("X-Forwarded-For", c.xff)
		}
		got, _ := oauthClientIP(e.NewContext(req, httptest.NewRecorder()))
		if got != c.want {
			t.Errorf("remote=%s xff=%q → %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}
