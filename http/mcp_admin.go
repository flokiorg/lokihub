package http

import (
	"errors"
	"net/http"
	"sort"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/mcp"
	"github.com/labstack/echo/v4"
)

const cfgKeyMCPPaused = "MCPPaused"

// cfgControls backs the owner's global MCP levers with the hub's persistent
// config, so a pause survives a restart. A read error is treated as paused
// (fail closed).
type cfgControls struct{ cfg config.Config }

func (c *cfgControls) Paused() bool {
	v, err := c.cfg.Get(cfgKeyMCPPaused, "")
	if err != nil {
		return true
	}
	return v == "1"
}

type mcpControlsBody struct {
	Paused *bool `json:"paused,omitempty"`
}

type mcpPendingView struct {
	ID      string `json:"id"`
	AppID   uint   `json:"appId"`
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
}

// registerMCPAdmin mounts the owner's MCP controls under the full-access admin
// token: the irreversible-action approval queue (list/approve/deny) and the
// global pause switch + redeem allow-list. The assistant cannot reach these —
// /mcp tokens are not admin JWTs.
func (httpSvc *HttpService) registerMCPAdmin(g *echo.Group) {
	httpSvc.registerMCPConnections(g)
	httpSvc.registerMCPConsent(g)
	g.GET("/mcp/pending", func(c echo.Context) error {
		if httpSvc.mcpSrv == nil {
			return c.JSON(http.StatusOK, []mcpPendingView{})
		}
		acts := httpSvc.mcpSrv.Pending().ListAll()
		out := make([]mcpPendingView, 0, len(acts))
		for _, a := range acts {
			out = append(out, mcpPendingView{ID: a.ID, AppID: a.AppID, Tool: a.Tool, Summary: a.Summary, Status: string(a.Status)})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return c.JSON(http.StatusOK, out)
	})
	g.POST("/mcp/pending/:id/approve", func(c echo.Context) error {
		if httpSvc.mcpSrv == nil {
			return echo.NewHTTPError(http.StatusNotFound, "no such action")
		}
		res, err := httpSvc.mcpSrv.ApprovePending(c.Request().Context(), c.Param("id"))
		switch {
		case errors.Is(err, mcp.ErrPaused):
			return echo.NewHTTPError(http.StatusConflict, "AI assistants are paused — unpause to approve")
		case errors.Is(err, mcp.ErrRevoked), errors.Is(err, mcp.ErrExpired), errors.Is(err, mcp.ErrInvalidToken):
			return echo.NewHTTPError(http.StatusGone, "the assistant that asked for this was revoked; the request was dropped")
		case errors.Is(err, mcp.ErrPendingExpired):
			return echo.NewHTTPError(http.StatusGone, "this request expired")
		case errors.Is(err, mcp.ErrNoPending):
			return echo.NewHTTPError(http.StatusNotFound, "no such action")
		case errors.Is(err, mcp.ErrPendingDenied):
			return echo.NewHTTPError(http.StatusConflict, "action was denied")
		case err != nil:
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]any{"status": "executed", "result": res})
	})
	g.POST("/mcp/pending/:id/deny", func(c echo.Context) error {
		if httpSvc.mcpSrv == nil {
			return echo.NewHTTPError(http.StatusNotFound, "no such action")
		}
		if err := httpSvc.mcpSrv.Pending().Deny(c.Param("id")); err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "no such action")
		}
		return c.NoContent(http.StatusNoContent)
	})
	g.GET("/mcp/controls", func(c echo.Context) error {
		paused := (&cfgControls{cfg: httpSvc.cfg}).Paused()
		return c.JSON(http.StatusOK, mcpControlsBody{Paused: &paused})
	})
	g.PUT("/mcp/controls", func(c echo.Context) error {
		var body mcpControlsBody
		if err := c.Bind(&body); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
		}
		if body.Paused != nil {
			v := ""
			if *body.Paused {
				v = "1"
			}
			if err := httpSvc.cfg.SetUpdate(cfgKeyMCPPaused, v, ""); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to save")
			}
		}
		return c.NoContent(http.StatusNoContent)
	})
}
