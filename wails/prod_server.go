//go:build !dev
// +build !dev

package wails

// StartDevServer is a no-op in production / non-dev builds.
// The background HTTP server is strictly for facilitating Vite proxy during development.
func StartDevServer(app *WailsApp) {
	// No-op
}
