//go:build dev
// +build dev

package wails

import (
	nethttp "net/http"

	"github.com/flokiorg/lokihub/logger"
	"github.com/labstack/echo/v4"
)

// StartDevServer starts a background HTTP server on the given port (default
// 1610) to allow Vite proxy requests to reach the backend during development.
// register mounts the routes to serve, so the startup-error page can reuse it.
// This code ONLY compiles when the 'dev' build tag is present.
func StartDevServer(port string, register func(e *echo.Echo)) {
	go func() {
		if port == "" {
			port = "1610"
		}

		logger.Logger.Info().Str("port", port).Msg("Starting background HTTP server for Wails Dev Mode")
		e := echo.New()
		e.HideBanner = true
		register(e)

		// Bind explicitly to 127.0.0.1, matching vite.config.ts's proxy target.
		// A bare ":port" binds an IPv6-wildcard socket, which macOS will happily
		// let coexist with another process's unrelated 127.0.0.1:port listener
		// (e.g. some other dev tool) instead of erroring — so Vite's proxied
		// requests silently vanish into that other process and hang forever,
		// with no indication anything is wrong. Binding the exact address Vite
		// targets makes a real conflict fail loudly here instead.
		if err := e.Start("127.0.0.1:" + port); err != nil && err != nethttp.ErrServerClosed {
			logger.Logger.Warn().Err(err).Msg("Background HTTP server failed to start (Dev Mode)")
		}
	}()
}
