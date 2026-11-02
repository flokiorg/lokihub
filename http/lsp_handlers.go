package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/lspservice"
)

// newLspClient builds the Hub's lspd dashboard client from the operator env
// (plan §3.2), or returns nil when no endpoint is configured (feature off).
// lspservice stays decoupled from lokihub's config — the env→Config mapping
// lives here.
func newLspClient(env *config.AppConfig) *lspservice.Client {
	if env == nil {
		return nil
	}
	return lspservice.New(lspservice.Config{
		Endpoint: env.LSPServiceEndpoint,
		Nsec:     env.LSPServiceNsec,
		Bunker:   env.LSPServiceBunker,
	})
}

// lspStatusToInfo maps the lspservice status onto the /api/info block.
func lspStatusToInfo(s lspservice.Status) *api.LSPServiceInfo {
	info := &api.LSPServiceInfo{
		Configured: s.Configured,
		Reachable:  s.Reachable,
		Attached:   s.Attached,
		Endpoint:   s.Endpoint,
		Pubkey:     s.Pubkey,
		Version:    s.Version,
		Alias:      s.Alias,
		Error:      s.Error,
	}
	if !s.CheckedAt.IsZero() {
		checkedAt := s.CheckedAt
		info.CheckedAt = &checkedAt
	}
	return info
}

// registerLspRoutes mounts the /api/lsp/* proxy. Every route is behind the
// Hub's existing JWT full-access group (requireFullAccess): the browser
// authenticates to the Hub over JWT, and the Hub calls lspd over NIP-98 via the
// lspservice client. The lspd dashboard methods are proxied 1:1.
//
// Single-operator v1 (audit note): all /api/lsp/* share one authz tier
// (full-access). TODO: if a second caller identity is ever introduced, split
// these into per-method authz (e.g. read-only summary/orders vs. full-access
// config/announce mutations) rather than the single gate here.
func (httpSvc *HttpService) registerLspRoutes(g *echo.Group) {
	g.GET("/lsp/status", httpSvc.lspStatusHandler)
	g.GET("/lsp/info", httpSvc.lspInfoHandler)
	g.GET("/lsp/summary", httpSvc.lspSummaryHandler)
	g.GET("/lsp/orders", httpSvc.lspOrdersHandler)
	g.GET("/lsp/orders/:orderId", httpSvc.lspOrderDetailHandler)
	g.GET("/lsp/peers", httpSvc.lspPeersHandler)
	g.GET("/lsp/jit-channels", httpSvc.lspJitChannelsHandler)
	g.GET("/lsp/nodes", httpSvc.lspNodesHandler)
	g.GET("/lsp/analytics", httpSvc.lspAnalyticsHandler)
	g.GET("/lsp/config", httpSvc.lspGetConfigHandler)
	g.PUT("/lsp/config", httpSvc.lspUpdateConfigHandler)
	g.GET("/lsp/announce-state", httpSvc.lspGetAnnounceStateHandler)
	g.POST("/lsp/announce-state", httpSvc.lspSetAnnounceStateHandler)
	g.POST("/lsp/recheck-announcement", httpSvc.lspRecheckAnnouncementHandler)
}

// lspResult writes a proxied JSON-RPC result, translating lspservice errors to
// HTTP status: not-configured -> 503, key mismatch / transport / rpc error ->
// 502 (the Hub reached the browser; lspd is the upstream that failed).
func lspResult(c echo.Context, raw json.RawMessage, err error) error {
	if err != nil {
		switch {
		case errors.Is(err, lspservice.ErrNotConfigured):
			return c.JSON(http.StatusServiceUnavailable, ErrorResponse{Message: "LSP service is not configured"})
		case errors.Is(err, lspservice.ErrForbidden):
			return c.JSON(http.StatusBadGateway, ErrorResponse{Message: "LSP rejected the request (admin key mismatch)"})
		default:
			return c.JSON(http.StatusBadGateway, ErrorResponse{Message: err.Error()})
		}
	}
	return c.JSONBlob(http.StatusOK, raw)
}

// client returns the configured lspd client or ErrNotConfigured.
func (httpSvc *HttpService) lspClientOrErr() (*lspservice.Client, error) {
	if httpSvc.lspClient == nil {
		return nil, lspservice.ErrNotConfigured
	}
	return httpSvc.lspClient, nil
}

func (httpSvc *HttpService) lspStatusHandler(c echo.Context) error {
	if httpSvc.lspClient == nil {
		return c.JSON(http.StatusOK, &api.LSPServiceInfo{Configured: false})
	}
	return c.JSON(http.StatusOK, lspStatusToInfo(httpSvc.lspClient.Status(c.Request().Context())))
}

func (httpSvc *HttpService) lspInfoHandler(c echo.Context) error {
	client, err := httpSvc.lspClientOrErr()
	if err != nil {
		return lspResult(c, nil, err)
	}
	info, err := client.Info(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusBadGateway, ErrorResponse{Message: err.Error()})
	}
	return c.JSON(http.StatusOK, info)
}

func (httpSvc *HttpService) lspSummaryHandler(c echo.Context) error {
	client, err := httpSvc.lspClientOrErr()
	if err != nil {
		return lspResult(c, nil, err)
	}
	raw, err := client.GetSummary(c.Request().Context())
	return lspResult(c, raw, err)
}

func (httpSvc *HttpService) lspOrdersHandler(c echo.Context) error {
	client, err := httpSvc.lspClientOrErr()
	if err != nil {
		return lspResult(c, nil, err)
	}
	raw, err := client.ListOrders(c.Request().Context())
	return lspResult(c, raw, err)
}

func (httpSvc *HttpService) lspOrderDetailHandler(c echo.Context) error {
	client, err := httpSvc.lspClientOrErr()
	if err != nil {
		return lspResult(c, nil, err)
	}
	raw, err := client.GetOrderDetail(c.Request().Context(), c.Param("orderId"))
	return lspResult(c, raw, err)
}

func (httpSvc *HttpService) lspPeersHandler(c echo.Context) error {
	client, err := httpSvc.lspClientOrErr()
	if err != nil {
		return lspResult(c, nil, err)
	}
	raw, err := client.ListPeers(c.Request().Context())
	return lspResult(c, raw, err)
}

func (httpSvc *HttpService) lspJitChannelsHandler(c echo.Context) error {
	client, err := httpSvc.lspClientOrErr()
	if err != nil {
		return lspResult(c, nil, err)
	}
	raw, err := client.ListJitChannels(c.Request().Context())
	return lspResult(c, raw, err)
}

func (httpSvc *HttpService) lspNodesHandler(c echo.Context) error {
	client, err := httpSvc.lspClientOrErr()
	if err != nil {
		return lspResult(c, nil, err)
	}
	raw, err := client.ListNodes(c.Request().Context())
	return lspResult(c, raw, err)
}

func (httpSvc *HttpService) lspAnalyticsHandler(c echo.Context) error {
	client, err := httpSvc.lspClientOrErr()
	if err != nil {
		return lspResult(c, nil, err)
	}
	raw, err := client.GetAnalytics(c.Request().Context())
	return lspResult(c, raw, err)
}

func (httpSvc *HttpService) lspGetConfigHandler(c echo.Context) error {
	client, err := httpSvc.lspClientOrErr()
	if err != nil {
		return lspResult(c, nil, err)
	}
	raw, err := client.GetConfig(c.Request().Context(), c.QueryParam("node"))
	return lspResult(c, raw, err)
}

func (httpSvc *HttpService) lspUpdateConfigHandler(c echo.Context) error {
	client, err := httpSvc.lspClientOrErr()
	if err != nil {
		return lspResult(c, nil, err)
	}
	// Forward the caller's params body ({config, node}) verbatim; lspd validates
	// it and strips/preserves secrets server-side (dashboard stripConfigSecrets).
	body, readErr := io.ReadAll(io.LimitReader(c.Request().Body, 1<<20))
	if readErr != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "failed to read body"})
	}
	raw, err := client.UpdateConfig(c.Request().Context(), json.RawMessage(body))
	return lspResult(c, raw, err)
}

func (httpSvc *HttpService) lspGetAnnounceStateHandler(c echo.Context) error {
	client, err := httpSvc.lspClientOrErr()
	if err != nil {
		return lspResult(c, nil, err)
	}
	raw, err := client.GetAnnounceState(c.Request().Context(), c.QueryParam("node"))
	return lspResult(c, raw, err)
}

func (httpSvc *HttpService) lspSetAnnounceStateHandler(c echo.Context) error {
	client, err := httpSvc.lspClientOrErr()
	if err != nil {
		return lspResult(c, nil, err)
	}
	var req struct {
		Action string `json:"action"`
		Node   string `json:"node"`
	}
	if bindErr := c.Bind(&req); bindErr != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "invalid request body"})
	}
	raw, err := client.SetAnnounceState(c.Request().Context(), req.Action, req.Node)
	return lspResult(c, raw, err)
}

func (httpSvc *HttpService) lspRecheckAnnouncementHandler(c echo.Context) error {
	client, err := httpSvc.lspClientOrErr()
	if err != nil {
		return lspResult(c, nil, err)
	}
	var req struct {
		Node string `json:"node"`
	}
	_ = c.Bind(&req)
	raw, err := client.RecheckAnnouncement(c.Request().Context(), req.Node)
	return lspResult(c, raw, err)
}
