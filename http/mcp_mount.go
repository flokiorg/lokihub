package http

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/flokiorg/lokihub/events"
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
	srv.SetPersistence(&gormExecutions{db: httpSvc.db}, &gormActivity{db: httpSvc.db})
	if err := srv.SetSpendStore(&gormSpends{db: httpSvc.db}); err != nil {
		logger.Logger.Error().Err(err).Msg("mcp: could not load the assistants' 24h spending window")
	}
	if httpSvc.eventPublisher != nil {
		httpSvc.eventPublisher.RegisterSubscriber(&mcpAppDeleted{srv: srv})
	}
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
	}).WithGeneration(httpSvc.mcpKeyGen)
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

// mcpAppDeleted clears the MCP layer's per-connection state when any app is
// deleted (owner revoke, assistant self-revoke, cascade).
type mcpAppDeleted struct{ srv *mcp.Server }

func (m *mcpAppDeleted) ConsumeEvent(_ context.Context, e *events.Event, _ map[string]interface{}) {
	if e == nil || e.Event != "nwc_app_deleted" {
		return
	}
	props, ok := e.Properties.(map[string]interface{})
	if !ok {
		return
	}
	switch id := props["id"].(type) {
	case uint:
		m.srv.ForgetApp(id)
	case int:
		if id > 0 {
			m.srv.ForgetApp(uint(id))
		}
	case float64:
		if id > 0 {
			m.srv.ForgetApp(uint(id))
		}
	}
}

// mcpKeyGenKey is the App.Metadata field holding an assistant's key generation.
const mcpKeyGenKey = "mcp_key_gen"

// mcpKeyGen reads an assistant's key generation (0 when never rotated or the
// app is unknown — an unknown app fails authentication later anyway).
func (httpSvc *HttpService) mcpKeyGen(appID uint) (uint64, error) {
	var app db.App
	if err := httpSvc.db.Select("metadata").Where("id = ?", appID).First(&app).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	var md map[string]any
	if len(app.Metadata) == 0 || json.Unmarshal(app.Metadata, &md) != nil {
		return 0, nil
	}
	if g, ok := md[mcpKeyGenKey].(float64); ok && g > 0 {
		return uint64(g), nil
	}
	return 0, nil
}
