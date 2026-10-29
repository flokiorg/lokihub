package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/stretchr/testify/assert"
)

// The unlock limiter keys a caller by its connection's address: a forged
// X-Forwarded-For or X-Real-IP buys no fresh attempt, while another peer still
// has its own.
func TestUnlockRateLimiter_IgnoresForwardedAddresses(t *testing.T) {
	e := echo.New()
	limiter := middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store:               middleware.NewRateLimiterMemoryStore(1),
		IdentifierExtractor: unlockLimiterIdentifier,
	})
	e.POST("/api/unlock", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) }, limiter)
	call := func(remote string, headers map[string]string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/unlock", nil)
		req.RemoteAddr = remote
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusNoContent, call("203.0.113.7:4000", nil))
	assert.Equal(t, http.StatusTooManyRequests, call("203.0.113.7:4001", map[string]string{"X-Forwarded-For": "198.51.100.1"}), "a forged forwarded address")
	assert.Equal(t, http.StatusTooManyRequests, call("203.0.113.7:4002", map[string]string{"X-Real-IP": "198.51.100.2"}), "a forged real IP")
	assert.Equal(t, http.StatusNoContent, call("203.0.113.8:4000", nil), "another peer has its own budget")
}
