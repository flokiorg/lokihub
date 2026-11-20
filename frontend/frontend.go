package frontend

import (
	"embed"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

//go:embed dist
var embeddedReactAssets embed.FS

func RegisterHandlers(e *echo.Echo) {
	e.Use(middleware.StaticWithConfig(middleware.StaticConfig{
		// API / MCP / OAuth paths are never the SPA: an unknown one must 404
		// (not 200 + index.html, which hides real errors from clients).
		Skipper: func(c echo.Context) bool {
			p := c.Request().URL.Path
			return strings.HasPrefix(p, "/api/") || p == "/mcp" || strings.HasPrefix(p, "/mcp/") ||
				strings.HasPrefix(p, "/oauth/") || strings.HasPrefix(p, "/.well-known/")
		},
		// Root directory from where the static content is served.
		Root: "dist",
		// Enable HTML5 mode by forwarding all not-found requests to root so that
		// SPA (single-page application) can handle the routing.
		HTML5:      true,
		Browse:     false,
		IgnoreBase: false,
		Filesystem: http.FS(embeddedReactAssets),
	}))
}
