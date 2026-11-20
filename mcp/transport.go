package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxRequestBodyBytes caps a single MCP request body (DoS guard, M-DOS-1). The
// SDK enforces it during the read, before parsing.
const maxRequestBodyBytes = 4 << 20 // 4 MiB

// sdkHandler is the raw MCP Streamable-HTTP handler. getServer reads the
// connection the bearer middleware authenticated and builds a per-session
// server bound to it; a missing connection yields 400 from the SDK (but the
// bearer middleware returns 401 first, so that path is unreachable in practice).
func (s *Server) sdkHandler() http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(func(r *http.Request) *mcpsdk.Server {
		conn, ok := ConnFrom(r.Context())
		if !ok {
			return nil
		}
		return s.serverForConn(conn)
	}, &mcpsdk.StreamableHTTPOptions{
		MaxRequestBodyBytes: maxRequestBodyBytes,
		// Idle sessions are reaped so a token cannot accumulate unbounded
		// per-session servers (audit MED-2).
		SessionTimeout: sessionIdleTimeout,
		// DisableLocalhostProtection stays false: the SDK rejects
		// cross-origin / rebinding requests to loopback for us.
	})
}

// Handler is the fully-wrapped MCP endpoint: bearer authentication in front of
// the Streamable-HTTP handler. Mount it at /mcp (server mode) or serve it on the
// loopback listener (desktop mode).
func (s *Server) Handler() http.Handler {
	return s.bearerAuth(bindSessionToConn(s.sdkHandler()))
}

// sessionIdleTimeout reaps idle MCP sessions.
const sessionIdleTimeout = 30 * time.Minute

// bindSessionToConn hands the SDK the authenticated connection as a TokenInfo
// whose UserID is the App id. The SDK then refuses any request that presents an
// existing Mcp-Session-Id under a different identity, so a token cannot drive
// another connection's session (audit MED-1). bearerAuth has already resolved
// and validated the token; this verifier only converts it.
func bindSessionToConn(next http.Handler) http.Handler {
	verify := func(ctx context.Context, _ string, r *http.Request) (*auth.TokenInfo, error) {
		conn, ok := ConnFrom(r.Context())
		if !ok {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: strconv.FormatUint(uint64(conn.AppID), 10), Scopes: conn.Scopes}, nil
	}
	return auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(next)
}

// bearerAuth validates the Authorization: Bearer token on every request (so
// revocation/expiry take effect per call) and stashes the resolved connection
// in the request context for getServer to read. Tokens are never accepted from
// the query string (MCP spec: access tokens MUST NOT appear in the URI).
func (s *Server) bearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r.Header.Get("Authorization"))
		if tok == "" {
			unauthorized(w, r)
			return
		}
		conn, err := s.authn.Resolve(r.Context(), tok)
		if err != nil {
			unauthorized(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(withConn(r.Context(), conn)))
	})
}

func bearerToken(authz string) string {
	const prefix = "Bearer "
	if len(authz) > len(prefix) && strings.EqualFold(authz[:len(prefix)], prefix) {
		return strings.TrimSpace(authz[len(prefix):])
	}
	return ""
}

// unauthorized writes a 401 with the RFC 9728 challenge, pointing an OAuth
// client at the protected-resource metadata so it can discover how to obtain a
// token (this is how remote connectors bootstrap OAuth).
func unauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(
		`Bearer realm="lokihub-mcp", resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`,
		RequestOrigin(r)))
	// Deliberately terse: never leak why (unknown vs revoked vs expired) or any
	// tool/catalogue detail to an unauthenticated caller.
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// Mount attaches the MCP endpoint to an existing Echo server (server mode).
// IMPORTANT: call this BEFORE the SPA's HTML5 static fallback is registered, or
// the fallback swallows /mcp (and, in M4, the /.well-known/* metadata routes).
func (s *Server) Mount(e *echo.Echo) {
	h := echo.WrapHandler(s.Handler())
	e.Any("/mcp", h)
	e.Any("/mcp/*", h)
}

// ListenLoopback starts a localhost-only HTTP listener serving the MCP endpoint
// (desktop mode, where the Wails process has no other network socket). The bind
// address is hard-coded to 127.0.0.1 so the listener is never reachable off the
// machine (M-BIND-1); callers must keep it opt-in / off by default.
func (s *Server) ListenLoopback(port int) (*http.Server, net.Addr, error) {
	mux := http.NewServeMux()
	mux.Handle("/mcp", s.Handler())
	mux.Handle("/mcp/", s.Handler())

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return srv, ln.Addr(), nil
}
