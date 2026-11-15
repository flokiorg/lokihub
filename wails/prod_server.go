//go:build !dev
// +build !dev

package wails

import "github.com/labstack/echo/v4"

// StartDevServer is a no-op in production / non-dev builds.
// The background HTTP server is strictly for facilitating Vite proxy during development.
func StartDevServer(port string, register func(e *echo.Echo)) {
	// No-op
}
