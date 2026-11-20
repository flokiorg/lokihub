package http

import (
	"encoding/json"
	"errors"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/mcp"
	"github.com/labstack/echo/v4"
)

// mcpOnUse returns the authenticator OnUse hook: keep the App's last_used_at
// fresh (throttled to once per minute per app) so the owner's Connections list
// shows real activity.
func (httpSvc *HttpService) mcpOnUse() func(uint) {
	var mu sync.Mutex
	last := map[uint]time.Time{}
	return func(appID uint) {
		now := time.Now()
		mu.Lock()
		if t, ok := last[appID]; ok && now.Sub(t) < time.Minute {
			mu.Unlock()
			return
		}
		last[appID] = now
		mu.Unlock()
		if err := httpSvc.db.Model(&db.App{}).Where("id = ?", appID).Update("last_used_at", &now).Error; err != nil {
			logger.Logger.Debug().Err(err).Uint("app_id", appID).Msg("mcp: last_used_at update failed")
		}
	}
}

type mcpConnectionView struct {
	AppID      uint                   `json:"appId"`
	AppPubkey  string                 `json:"appPubkey"`
	Name       string                 `json:"name"`
	Groups     map[mcp.Group]mcp.Mode `json:"groups"`
	CreatedAt  time.Time              `json:"createdAt"`
	LastUsedAt *time.Time             `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time             `json:"expiresAt,omitempty"`
}

// registerMCPConnections mounts the owner's AI-assistant connection API under
// the full-access admin token:
//
//	POST /api/mcp/connections {name, groups{group:"read"|"write"}, expiresInDays}
//	  → {appId, appPubkey, token}   (token shown once; never stored)
//	GET  /api/mcp/connections → [{appId, appPubkey, name, groups, ...}]
//
// Revocation reuses DELETE /api/apps/:pubkey. Rotation = mint a new connection
// with the same groups, then revoke the old one (the HMAC token is per-App).
func (httpSvc *HttpService) registerMCPConnections(g *echo.Group) {
	g.POST("/mcp/connections", func(c echo.Context) error {
		var req struct {
			Name          string                 `json:"name"`
			Groups        map[mcp.Group]mcp.Mode `json:"groups"`
			ExpiresInDays int                    `json:"expiresInDays"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
		}
		var exp *time.Time
		if req.ExpiresInDays > 0 {
			t := time.Now().AddDate(0, 0, req.ExpiresInDays)
			exp = &t
		}
		id, tok, err := mcp.CreateConnection(httpSvc.mcpAppCreator(), httpSvc.mcpTokens(), req.Name, req.Groups, exp)
		if err != nil {
			if errors.Is(err, mcp.ErrNoSecret) {
				return echo.NewHTTPError(http.StatusServiceUnavailable, "unlock the hub first")
			}
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		pubkey := ""
		if app := httpSvc.appsSvc.GetAppById(id); app != nil {
			pubkey = app.AppPubkey
		}
		return c.JSON(http.StatusOK, map[string]any{"appId": id, "appPubkey": pubkey, "token": tok})
	})
	g.GET("/mcp/connections", func(c echo.Context) error {
		var apps []db.App
		if err := httpSvc.db.Where("kind = ?", mcp.AppKindMCP).Order("created_at desc").Find(&apps).Error; err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to list connections")
		}
		out := make([]mcpConnectionView, 0, len(apps))
		for _, a := range apps {
			var perms []db.AppPermission
			if err := httpSvc.db.Where("app_id = ?", a.ID).Find(&perms).Error; err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to list connections")
			}
			scopes := make([]string, 0, len(perms))
			for _, p := range perms {
				scopes = append(scopes, p.Scope)
			}
			out = append(out, mcpConnectionView{
				AppID: a.ID, AppPubkey: a.AppPubkey, Name: a.Name,
				Groups: mcp.GrantsFromScopes(scopes), CreatedAt: a.CreatedAt,
				LastUsedAt: a.LastUsedAt, ExpiresAt: a.ExpiresAt,
			})
		}
		return c.JSON(http.StatusOK, out)
	})

	// Edit an assistant: rename it, change what it may do, and/or rotate its
	// key (the old key stops working at once). Owner-only (critic G4/C1).
	//	PATCH /api/mcp/connections/:appId {name?, groups?, rotateKey?}
	g.PATCH("/mcp/connections/:appId", func(c echo.Context) error {
		id, err := strconv.ParseUint(c.Param("appId"), 10, 32)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
		}
		var req struct {
			Name      *string                 `json:"name"`
			Groups    *map[mcp.Group]mcp.Mode `json:"groups"`
			RotateKey bool                    `json:"rotateKey"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
		}
		var token string
		err = httpSvc.db.Transaction(func(tx *gorm.DB) error {
			var app db.App
			if err := tx.Where("id = ? AND kind = ?", id, mcp.AppKindMCP).First(&app).Error; err != nil {
				return echo.NewHTTPError(http.StatusNotFound, "no such assistant")
			}
			if req.Name != nil {
				name := mcp.CleanConnectionName(*req.Name)
				if name == "" {
					return echo.NewHTTPError(http.StatusBadRequest, "name required")
				}
				var n int64
				tx.Model(&db.App{}).Where("name = ? AND id != ?", name, app.ID).Count(&n)
				if n > 0 {
					return echo.NewHTTPError(http.StatusConflict, "another connection already has that name")
				}
				if err := tx.Model(&app).Update("name", name).Error; err != nil {
					return err
				}
			}
			if req.Groups != nil {
				scopes, err := mcp.ScopesFor(*req.Groups)
				if err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, err.Error())
				}
				if err := tx.Where("app_id = ?", app.ID).Delete(&db.AppPermission{}).Error; err != nil {
					return err
				}
				for _, sc := range scopes {
					if err := tx.Create(&db.AppPermission{AppId: app.ID, Scope: sc, BudgetRenewal: "never", ExpiresAt: app.ExpiresAt}).Error; err != nil {
						return err
					}
				}
			}
			if req.RotateKey {
				md := map[string]any{}
				if len(app.Metadata) > 0 {
					_ = json.Unmarshal(app.Metadata, &md)
				}
				g, _ := md[mcpKeyGenKey].(float64)
				md[mcpKeyGenKey] = g + 1
				b, _ := json.Marshal(md)
				if err := tx.Model(&app).Update("metadata", datatypes.JSON(b)).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			var he *echo.HTTPError
			if errors.As(err, &he) {
				return he
			}
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to update the assistant")
		}
		if req.RotateKey {
			if token, err = httpSvc.mcpTokens().Mint(uint(id)); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to issue the new key")
			}
		}
		return c.JSON(http.StatusOK, map[string]string{"token": token})
	})
}
