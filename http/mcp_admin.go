package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/mcp"
	"github.com/labstack/echo/v4"
)

const (
	cfgKeyMCPPaused         = "MCPPaused"
	cfgKeyMCPRedeemAllowlst = "MCPRedeemAllowlist"
)

// cfgControls backs the owner's global MCP levers with the hub's persistent
// config, so a pause survives a restart. A read error is treated as paused /
// not-allow-listed (fail closed).
type cfgControls struct{ cfg config.Config }

func (c *cfgControls) Paused() bool {
	v, err := c.cfg.Get(cfgKeyMCPPaused, "")
	if err != nil {
		return true
	}
	return v == "1"
}

func (c *cfgControls) allowlist() ([]string, error) {
	v, err := c.cfg.Get(cfgKeyMCPRedeemAllowlst, "")
	if err != nil || v == "" {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *cfgControls) RedeemAllowed(addr string) bool {
	list, err := c.allowlist()
	if err != nil {
		return false
	}
	for _, a := range list {
		if a == addr {
			return true
		}
	}
	return false
}

type mcpControlsBody struct {
	Paused          *bool    `json:"paused,omitempty"`
	RedeemAllowlist []string `json:"redeemAllowlist,omitempty"`
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
		ctl := &cfgControls{cfg: httpSvc.cfg}
		list, _ := ctl.allowlist()
		paused := ctl.Paused()
		return c.JSON(http.StatusOK, mcpControlsBody{Paused: &paused, RedeemAllowlist: list})
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
		if body.RedeemAllowlist != nil {
			norm := make([]string, 0, len(body.RedeemAllowlist))
			for _, a := range body.RedeemAllowlist {
				if a = mcp.NormalizeAddress(a); a != "" && !strings.ContainsAny(a, " \t\n") {
					norm = append(norm, a)
				}
			}
			b, _ := json.Marshal(norm)
			if err := httpSvc.cfg.SetUpdate(cfgKeyMCPRedeemAllowlst, string(b), ""); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to save")
			}
		}
		return c.NoContent(http.StatusNoContent)
	})
}
