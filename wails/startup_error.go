package wails

import (
	"context"
	"embed"
	nethttp "net/http"
	"os"

	"github.com/flokiorg/lokihub/appversion"
	"github.com/flokiorg/lokihub/logger"
	"github.com/labstack/echo/v4"
	"github.com/wailsapp/wails/v2"
)

// StartupErrorCode marks an API response as "the backend failed to start", so
// the frontend shows the startup-error screen instead of its generic error page.
// Keep in sync with frontend/src/utils/request.ts.
const StartupErrorCode = "startup_failed"

type startupErrorResponse struct {
	Message string `json:"message"`
	Code    string `json:"code"`
	Version string `json:"version"`
}

func registerStartupErrorRoutes(startupErr error) func(e *echo.Echo) {
	return func(e *echo.Echo) {
		e.HideBanner = true
		e.HidePort = true
		// The service never came up, so every API call gets the same answer.
		e.Any("/api/*", func(c echo.Context) error {
			return c.JSON(nethttp.StatusServiceUnavailable, startupErrorResponse{
				Message: startupErr.Error(),
				Code:    StartupErrorCode,
				Version: appversion.Tag,
			})
		})
	}
}

// LaunchStartupErrorApp opens the normal window, but backed only by a handler
// that reports why the service failed to start. The frontend renders that as
// a full-page error with a close button, instead of the app exiting silently.
// It blocks until the user closes the window.
func LaunchStartupErrorApp(startupErr error, assets embed.FS, appIcon []byte) {
	register := registerStartupErrorRoutes(startupErr)

	// Same as the normal app: lets `wails dev` reach this handler through the
	// Vite proxy. The real service never started, so there's no config.AppConfig
	// to read PORT from here (unlike LaunchWailsApp) — read the env var directly;
	// StartDevServer falls back to 1610 itself if it's unset.
	StartDevServer(os.Getenv("PORT"), register)

	e := echo.New()
	register(e)

	opts := windowOptions(assets, appIcon, e)
	opts.OnStartup = func(ctx context.Context) {
		scheduleDevReload(ctx)
	}

	// No OnBeforeClose or tray here: closing the window really quits.
	if err := wails.Run(opts); err != nil {
		logger.Logger.Error().Err(err).Msg("failed to run Wails startup-error app")
	}
}
