package http

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flokiorg/lokihub/mcp"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// consentPath is the SPA route of the owner consent screen. It deliberately does
// not live under /oauth (those paths are served by the hub, not the SPA).
const consentPath = "/connect-assistant"

func oauthError(c echo.Context, status int, err error, desc string) error {
	code := "invalid_request"
	for _, e := range []error{mcp.ErrOAuthInvalidClient, mcp.ErrOAuthInvalidGrant, mcp.ErrOAuthInvalidTarget, mcp.ErrOAuthInvalidRequest} {
		if errors.Is(err, e) {
			code = e.Error()
			break
		}
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	body := map[string]string{"error": code}
	if desc != "" {
		body["error_description"] = desc
	}
	return c.JSON(status, body)
}

// registerMCPOAuth mounts the public OAuth 2.1 surface (MUST be before the SPA
// HTML5 fallback, or discovery silently returns the SPA shell):
//
//	GET  /.well-known/oauth-protected-resource[/mcp]   RFC 9728
//	GET  /.well-known/oauth-authorization-server       RFC 8414
//	POST /oauth/register                               RFC 7591 (rate-limited)
//	GET  /oauth/authorize                              validate → park → consent screen
//	POST /oauth/token                                  code + PKCE → bearer (rate-limited)
func (httpSvc *HttpService) registerMCPOAuth(e *echo.Echo) {
	if httpSvc.mcpOAuth == nil {
		httpSvc.mcpOAuth = mcp.NewOAuthStore()
	}
	store := httpSvc.mcpOAuth
	limited := middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store:               middleware.NewRateLimiterMemoryStore(2),
		IdentifierExtractor: oauthClientIP,
	})
	// Unauthenticated endpoints get a small body cap (audit MED-3): no
	// legitimate OAuth request is anywhere near 16 KiB.
	small := middleware.BodyLimit("16K")

	prm := func(c echo.Context) error { return c.JSON(http.StatusOK, mcp.ProtectedResourceMetadata(c.Request())) }
	e.GET("/.well-known/oauth-protected-resource", prm)
	e.GET("/.well-known/oauth-protected-resource/mcp", prm)
	e.GET("/.well-known/oauth-authorization-server", func(c echo.Context) error {
		return c.JSON(http.StatusOK, mcp.AuthorizationServerMetadata(c.Request()))
	})

	e.POST("/oauth/register", func(c echo.Context) error {
		var req struct {
			ClientName   string   `json:"client_name"`
			RedirectURIs []string `json:"redirect_uris"`
		}
		if err := c.Bind(&req); err != nil {
			return oauthError(c, http.StatusBadRequest, mcp.ErrOAuthInvalidRequest, "invalid JSON")
		}
		cl, err := store.RegisterFrom(req.ClientName, req.RedirectURIs, oauthClientIP0(c))
		if err != nil {
			return oauthError(c, http.StatusBadRequest, err, err.Error())
		}
		return c.JSON(http.StatusCreated, map[string]any{
			"client_id":                  cl.ID,
			"client_name":                cl.Name,
			"redirect_uris":              cl.RedirectURIs,
			"token_endpoint_auth_method": "none",
			"grant_types":                []string{"authorization_code"},
			"response_types":             []string{"code"},
			"client_id_issued_at":        cl.CreatedAt.Unix(),
		})
	}, limited, small)

	e.GET("/oauth/authorize", func(c echo.Context) error {
		q := c.QueryParams()
		req := mcp.AuthorizeRequest{
			ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), State: q.Get("state"),
			Challenge: q.Get("code_challenge"), ChallengeMethod: q.Get("code_challenge_method"),
			Resource: q.Get("resource"),
		}
		if q.Get("response_type") != "code" {
			return oauthError(c, http.StatusBadRequest, mcp.ErrOAuthInvalidRequest, "response_type must be code")
		}
		// Errors here are shown, not redirected: an unverified client/redirect
		// must never receive a redirect (open-redirector protection).
		id, err := store.BeginConsentFrom(req, mcp.ResourceURL(c.Request()), oauthClientIP0(c))
		if err != nil {
			return oauthError(c, http.StatusBadRequest, err, err.Error())
		}
		return c.Redirect(http.StatusFound, consentPath+"?consent="+url.QueryEscape(id))
	}, limited)

	e.POST("/oauth/token", func(c echo.Context) error {
		f := func(k string) string { return c.FormValue(k) }
		if f("grant_type") != "authorization_code" {
			return oauthError(c, http.StatusBadRequest, errors.New("unsupported_grant_type"), "")
		}
		appID, err := store.Exchange(f("code"), f("client_id"), f("redirect_uri"), f("code_verifier"), f("resource"))
		if err != nil {
			return oauthError(c, http.StatusBadRequest, err, "")
		}
		tok, err := httpSvc.mcpTokens().Mint(appID)
		if err != nil {
			return oauthError(c, http.StatusServiceUnavailable, err, "hub is locked")
		}
		scopes := []string{}
		if app := httpSvc.appsSvc.GetAppById(appID); app != nil {
			var perms []struct{ Scope string }
			httpSvc.db.Table("app_permissions").Select("scope").Where("app_id = ?", appID).Scan(&perms)
			for _, p := range perms {
				scopes = append(scopes, p.Scope)
			}
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(http.StatusOK, map[string]any{
			"access_token": tok,
			"token_type":   "Bearer",
			"scope":        strings.Join(scopes, " "),
		})
	}, limited, small)
}

// registerMCPConsent mounts the owner's consent API (full-access JWT): the SPA
// consent screen reads the parked request by id, then approves (creating the
// connection with the owner-chosen groups) or denies.
func (httpSvc *HttpService) registerMCPConsent(g *echo.Group) {
	g.GET("/mcp/oauth/consent/:id", func(c echo.Context) error {
		if httpSvc.mcpOAuth == nil {
			return echo.NewHTTPError(http.StatusNotFound, "no such request")
		}
		v, err := httpSvc.mcpOAuth.Consent(c.Param("id"))
		if err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "this connection request expired — start again from your assistant")
		}
		return c.JSON(http.StatusOK, v)
	})
	g.POST("/mcp/oauth/consent/:id/approve", func(c echo.Context) error {
		if httpSvc.mcpOAuth == nil {
			return echo.NewHTTPError(http.StatusNotFound, "no such request")
		}
		var body struct {
			Name          string                 `json:"name"`
			Groups        map[mcp.Group]mcp.Mode `json:"groups"`
			ExpiresInDays int                    `json:"expiresInDays"`
		}
		if err := c.Bind(&body); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
		}
		if _, err := httpSvc.mcpOAuth.Consent(c.Param("id")); err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "this connection request expired — start again from your assistant")
		}
		// Same canonicalisation as every other assistant name, so a lookalike
		// cannot pass the duplicate check (pentest E MED-2).
		name := mcp.CleanConnectionName(body.Name)
		scopes, err := mcp.ScopesFor(body.Groups)
		if err != nil || name == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "choose a name and at least one area")
		}
		if httpSvc.mcpNameLooksTaken(name, 0) {
			return echo.NewHTTPError(http.StatusConflict, "choose a different name: it looks like another assistant's, or mixes letters from different alphabets")
		}
		if body.ExpiresInDays < 0 || body.ExpiresInDays > 365 {
			return echo.NewHTTPError(http.StatusBadRequest, "expiry must be 0 (never) to 365 days")
		}
		var exp *time.Time
		if body.ExpiresInDays > 0 {
			t := time.Now().Add(time.Duration(body.ExpiresInDays) * 24 * time.Hour)
			exp = &t
		}
		appID, err := httpSvc.mcpAppCreator()(name, exp, scopes)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		redirect, err := httpSvc.mcpOAuth.ApproveConsent(c.Param("id"), appID, mcp.RequestOrigin(c.Request()))
		if err != nil {
			// Roll back the connection we just created: no token will ever reach it.
			if app := httpSvc.appsSvc.GetAppById(appID); app != nil {
				_ = httpSvc.api.DeleteApp(app)
			}
			return echo.NewHTTPError(http.StatusNotFound, "this connection request expired — start again from your assistant")
		}
		return c.JSON(http.StatusOK, map[string]string{"redirect": redirect})
	})
	g.POST("/mcp/oauth/consent/:id/deny", func(c echo.Context) error {
		if httpSvc.mcpOAuth == nil {
			return echo.NewHTTPError(http.StatusNotFound, "no such request")
		}
		redirect, err := httpSvc.mcpOAuth.DenyConsent(c.Param("id"), mcp.RequestOrigin(c.Request()))
		if err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "no such request")
		}
		return c.JSON(http.StatusOK, map[string]string{"redirect": redirect})
	})
}

// oauthClientIP keys the OAuth rate limit and registration bound by the real
// client (see clientIP: forwarded headers count only from a trusted proxy).
func oauthClientIP(c echo.Context) (string, error) { return clientIP(c), nil }

func oauthClientIP0(c echo.Context) string { ip, _ := oauthClientIP(c); return ip }
