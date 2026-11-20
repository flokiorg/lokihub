package http

import (
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/mcp"
	"github.com/labstack/echo/v4"
)

const (
	cfgKeyMCPPaused = "MCPPaused"
	// cfgKeyMCPApprovalLimit: above this many loki, an assistant's payment or
	// transfer waits for the owner's approval (D22). "" = the default.
	cfgKeyMCPApprovalLimit = "MCPApprovalLimitLoki"
	// CfgKeyMCPLoopbackPort is the desktop owner's setting for the local MCP
	// listener on 127.0.0.1 ("" / "0" = off, the default).
	CfgKeyMCPLoopbackPort = "MCPLoopbackPort"
)

// MCPLoopbackController is provided by the desktop app: it opens or closes the
// 127.0.0.1 MCP listener. Server builds register none (the switch is hidden).
type MCPLoopbackController interface {
	// Apply listens on port (0 = stop). It returns an error, leaving the
	// previous state, if the port cannot be opened.
	Apply(port int) error
	// Port is the port currently listening (0 = off).
	Port() int
}

// SetMCPLoopbackController registers the desktop's loopback controller.
func (httpSvc *HttpService) SetMCPLoopbackController(c MCPLoopbackController) {
	httpSvc.mcpLoopback = c
}

// cfgControls backs the owner's global MCP levers with the hub's persistent
// config, so a pause survives a restart. A read error is treated as paused
// (fail closed).
type cfgControls struct{ cfg config.Config }

// ApprovalLimitMloki implements mcp.ApprovalLimiter. A read error falls back
// to 0 (every payment needs approval: fail closed).
func (c *cfgControls) ApprovalLimitMloki() uint64 {
	v, err := c.cfg.Get(cfgKeyMCPApprovalLimit, "")
	if err != nil {
		return 0
	}
	if v == "" {
		return mcp.DefaultApprovalLimitMloki
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n > 1<<40 {
		return 0
	}
	return n * 1000
}

func (c *cfgControls) Paused() bool {
	v, err := c.cfg.Get(cfgKeyMCPPaused, "")
	if err != nil {
		return true
	}
	return v == "1"
}

type mcpControlsBody struct {
	Paused            *bool `json:"paused,omitempty"`
	LoopbackSupported *bool `json:"loopbackSupported,omitempty"`
	LoopbackPort      *int  `json:"loopbackPort,omitempty"`
	// ApprovalLimitLoki: payments/transfers above it need owner approval.
	ApprovalLimitLoki *uint64 `json:"approvalLimitLoki,omitempty"`
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
	// The owner's view of what each assistant did (newest first).
	g.GET("/mcp/activity", func(c echo.Context) error {
		if httpSvc.mcpSrv == nil {
			return c.JSON(http.StatusOK, []mcp.ActivityEntry{})
		}
		appID, _ := strconv.ParseUint(c.QueryParam("app_id"), 10, 32)
		limit, _ := strconv.Atoi(c.QueryParam("limit"))
		return c.JSON(http.StatusOK, httpSvc.mcpSrv.Activity().List(uint(appID), limit))
	})
	g.GET("/mcp/controls", func(c echo.Context) error {
		ctl := &cfgControls{cfg: httpSvc.cfg}
		paused := ctl.Paused()
		limit := ctl.ApprovalLimitMloki() / 1000
		body := mcpControlsBody{Paused: &paused, ApprovalLimitLoki: &limit}
		if httpSvc.mcpLoopback != nil {
			yes, port := true, httpSvc.mcpLoopback.Port()
			body.LoopbackSupported, body.LoopbackPort = &yes, &port
		}
		return c.JSON(http.StatusOK, body)
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
		if body.ApprovalLimitLoki != nil {
			if *body.ApprovalLimitLoki > 1_000_000_000 {
				return echo.NewHTTPError(http.StatusBadRequest, "limit too large")
			}
			if err := httpSvc.cfg.SetUpdate(cfgKeyMCPApprovalLimit, strconv.FormatUint(*body.ApprovalLimitLoki, 10), ""); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to save")
			}
		}
		if body.LoopbackPort != nil {
			if httpSvc.mcpLoopback == nil {
				return echo.NewHTTPError(http.StatusBadRequest, "local access is only available in the desktop app")
			}
			port := *body.LoopbackPort
			if port != 0 && (port < 1024 || port > 65535) {
				return echo.NewHTTPError(http.StatusBadRequest, "port must be between 1024 and 65535 (or 0 to turn local access off)")
			}
			if err := httpSvc.mcpLoopback.Apply(port); err != nil {
				return echo.NewHTTPError(http.StatusConflict, "could not open that port: "+err.Error())
			}
			if err := httpSvc.cfg.SetUpdate(CfgKeyMCPLoopbackPort, strconv.Itoa(port), ""); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to save")
			}
		}
		return c.NoContent(http.StatusNoContent)
	})
}
