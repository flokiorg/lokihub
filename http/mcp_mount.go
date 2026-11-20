package http

import (
	"context"
	"errors"
	"time"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/mcp"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// dbAppLoader loads an MCP connection's App (and its scopes) from the hub DB on
// every call, so revocation (the App row is gone) and re-scoping take effect at
// once. It implements mcp.AppLoader.
type dbAppLoader struct{ db *gorm.DB }

func (l *dbAppLoader) LoadApp(_ context.Context, appID uint) (mcp.AppInfo, bool, error) {
	var app db.App
	if err := l.db.Where("id = ?", appID).First(&app).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return mcp.AppInfo{}, false, nil
		}
		return mcp.AppInfo{}, false, err
	}
	var perms []db.AppPermission
	if err := l.db.Where("app_id = ?", appID).Find(&perms).Error; err != nil {
		return mcp.AppInfo{}, false, err
	}
	scopes := make([]string, 0, len(perms))
	for _, p := range perms {
		scopes = append(scopes, p.Scope)
	}
	return mcp.AppInfo{
		Name:      app.Name,
		Scopes:    scopes,
		Kind:      app.Kind,
		ExpiresAt: app.ExpiresAt,
	}, true, nil
}

// registerMCP mounts the MCP Streamable-HTTP endpoint at /mcp. The token store
// derives from the hub's signing secret (fetched per request, so it is inert
// until the hub is unlocked and invalidates all tokens on a secret rotation).
// MUST be called before the SPA HTML5 fallback.
func (httpSvc *HttpService) registerMCP(e *echo.Echo) {
	authn := &mcp.StandardAuthenticator{
		Store: httpSvc.mcpTokens(),
		Apps:  &dbAppLoader{db: httpSvc.db},
		OnUse: httpSvc.mcpOnUse(),
	}
	srv := mcp.NewServer(httpSvc.api, authn)
	srv.SetAppResolver(httpSvc.appsSvc.GetAppById)
	srv.SetControls(&cfgControls{cfg: httpSvc.cfg})
	srv.Mount(e)
	httpSvc.mcpSrv = srv
	httpSvc.registerMCPOAuth(e)
	logger.Logger.Info().Msg("mcp: /mcp endpoint mounted")
}

// mcpTokens is the hub's MCP bearer-token store, keyed by the hub signing secret
// (fetched per use). The mount and the connection-mint path share it so a minted
// token verifies at /mcp.
func (httpSvc *HttpService) mcpTokens() *mcp.HMACTokenStore {
	return mcp.NewHMACTokenStore(func() ([]byte, error) {
		s, err := httpSvc.cfg.GetJWTSecret()
		return []byte(s), err
	})
}

// mcpAppCreator persists an MCP connection as a kind="mcp" App with the given
// mcp:* scopes (no budget, no NWC methods — the scopes map to none).
func (httpSvc *HttpService) mcpAppCreator() mcp.AppCreator {
	return func(name string, expiresAt *time.Time, scopes []string) (uint, error) {
		app, _, err := httpSvc.appsSvc.CreateApp(name, "", 0, "never", expiresAt, scopes, mcp.AppKindMCP, nil, "", nil)
		if err != nil {
			return 0, err
		}
		return app.ID, nil
	}
}

// MCPServer returns the hub's MCP server (nil before RegisterSharedRoutes). The
// desktop app uses it to serve the opt-in loopback listener.
func (httpSvc *HttpService) MCPServer() *mcp.Server { return httpSvc.mcpSrv }
