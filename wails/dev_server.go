//go:build dev
// +build dev

package wails

import (
	nethttp "net/http"

	"github.com/flokiorg/lokihub/logger"
	"github.com/labstack/echo/v4"
)

// StartDevServer starts a background HTTP server on port 1610 (or configured port)
// to allow Vite proxy requests to reach the backend during development.
// This code ONLY compiles when the 'dev' build tag is present.
func StartDevServer(app *WailsApp) {
	go func() {
		// Default to port 1610 if not configured
		port := "1610"
		if app.svc.GetConfig().GetEnv().Port != "" {
			port = app.svc.GetConfig().GetEnv().Port
		}

		logger.Logger.Info().Str("port", port).Msg("Starting background HTTP server for Wails Dev Mode")
		e := echo.New()
		e.HideBanner = true
		app.httpSvc.RegisterSharedRoutes(e)

		if err := e.Start(":" + port); err != nil && err != nethttp.ErrServerClosed {
			logger.Logger.Warn().Err(err).Msg("Background HTTP server failed to start (Dev Mode)")
		}
	}()
}
