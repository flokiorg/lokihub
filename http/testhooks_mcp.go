//go:build testhooks

package http

import (
	"net/http"
	"time"

	"github.com/flokiorg/lokihub/mcp"
	"github.com/labstack/echo/v4"
)

// registerMCPTestHooks mounts POST /api/test/mcp/connection, which mints an MCP
// connection (kind=mcp App + bearer token) so integration tests can dial /mcp
// before the Connect-AI UI exists. testhooks builds only.
func (httpSvc *HttpService) registerMCPTestHooks(g *echo.Group) {
	g.POST("/mcp/connection", func(c echo.Context) error {
		var req struct {
			Name         string                 `json:"name"`
			Groups       map[mcp.Group]mcp.Mode `json:"groups"`
			ExpiresInSec int64                  `json:"expires_in_sec"`
		}
		if err := c.Bind(&req); err != nil {
			return hookError(c, err)
		}
		var exp *time.Time
		if req.ExpiresInSec > 0 {
			t := time.Now().Add(time.Duration(req.ExpiresInSec) * time.Second)
			exp = &t
		}
		id, tok, err := mcp.CreateConnection(httpSvc.mcpAppCreator(), httpSvc.mcpTokens(), req.Name, req.Groups, exp)
		if err != nil {
			return hookError(c, err)
		}
		pubkey := ""
		if app := httpSvc.appsSvc.GetAppById(id); app != nil {
			pubkey = app.AppPubkey
		}
		return c.JSON(http.StatusOK, map[string]any{"app_id": id, "app_pubkey": pubkey, "token": tok})
	})
}
