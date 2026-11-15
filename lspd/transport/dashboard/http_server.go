package dashboard

import (
	"net/http"
	"time"
)

// Timeouts of the dashboard listener. It shares the process with the LSPS
// handlers and the HTLC interceptor, so a slow or idle client must not be
// able to hold connections open. WebSocket upgrades clear these deadlines
// on the hijacked connection.
const (
	ReadHeaderTimeout = 10 * time.Second
	ReadTimeout       = 30 * time.Second
	WriteTimeout      = 30 * time.Second
	IdleTimeout       = 120 * time.Second
	MaxHeaderBytes    = 64 << 10
)

// NewHTTPServer returns the dashboard's http.Server on addr with bounded
// header, read, write and idle times.
func NewHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
	}
}
