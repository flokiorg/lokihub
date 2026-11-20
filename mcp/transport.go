package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
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
func (s *Server) sdkHandler(behindProxy bool) http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(func(r *http.Request) *mcpsdk.Server {
		conn, ok := ConnFrom(r.Context())
		if !ok {
			return nil
		}
		return s.cachedServerFor(conn)
	}, &mcpsdk.StreamableHTTPOptions{
		MaxRequestBodyBytes: maxRequestBodyBytes,
		// Idle sessions are reaped so a token cannot accumulate unbounded
		// per-session servers (audit MED-2).
		SessionTimeout: sessionIdleTimeout,
		// Localhost (DNS-rebinding) protection: kept on the desktop loopback
		// listener, where a browser page could otherwise reach 127.0.0.1. On
		// the hub's own endpoint it would refuse every request from a
		// same-host reverse proxy (loopback peer, public Host — critic O7), and
		// a bearer token is mandatory there, which a rebinding page lacks.
		DisableLocalhostProtection: behindProxy,
	})
}

// Handler is the fully-wrapped MCP endpoint: bearer authentication in front of
// the Streamable-HTTP handler. Mount it at /mcp (server mode) or serve it on the
// loopback listener (desktop mode).
func (s *Server) Handler() http.Handler {
	return s.handler(false)
}

func (s *Server) handler(behindProxy bool) http.Handler {
	return s.bearerAuth(bindSessionToConn(s.sessionCap(s.sdkHandler(behindProxy))))
}

// maxSessionsPerConn bounds concurrent MCP sessions per connection (re-audit
// N4): a token cannot pile up sessions (each holds state until it idles out).
const maxSessionsPerConn = 10

type connServer struct {
	scopes   string
	srv      *mcpsdk.Server
	starting int // new-session requests in flight (guarded by Server.srvMu)
}

// cachedServerFor returns the connection's MCP server, building it once per
// (connection, scope set). The SDK asks for a server on EVERY request (protocol
// version check), so building per request re-reflected ~66 tool schemas each
// time — a CPU amplification (re-audit N4). Tool handlers re-check the
// connection's CURRENT scopes on every call (authorizeCall), so a cached tool
// list can never widen access; a rescope rebuilds the advertised list.
func (s *Server) cachedServerFor(conn *Conn) *mcpsdk.Server {
	key := scopesKey(conn.Scopes)
	s.srvMu.Lock()
	defer s.srvMu.Unlock()
	if cs := s.srvByApp[conn.AppID]; cs != nil && cs.scopes == key {
		return cs.srv
	}
	cs := &connServer{scopes: key, srv: s.serverForConn(conn)}
	if old := s.srvByApp[conn.AppID]; old != nil {
		cs.starting = old.starting
	}
	s.srvByApp[conn.AppID] = cs
	return cs.srv
}

func scopesKey(sc []string) string {
	c := append([]string(nil), sc...)
	sort.Strings(c)
	return strings.Join(c, " ")
}

// liveSessions counts the connection's open sessions plus new-session requests
// still being set up (so a burst cannot race past the cap).
func (s *Server) liveSessionsLocked(appID uint) int {
	cs := s.srvByApp[appID]
	if cs == nil {
		return 0
	}
	n := cs.starting
	for range cs.srv.Sessions() {
		n++
	}
	return n
}

// sessionCap refuses a NEW session (a POST without Mcp-Session-Id) once the
// connection already holds maxSessionsPerConn.
func (s *Server) sessionCap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, ok := ConnFrom(r.Context())
		if !ok || r.Method != http.MethodPost || r.Header.Get("Mcp-Session-Id") != "" {
			next.ServeHTTP(w, r)
			return
		}
		s.cachedServerFor(conn)
		s.srvMu.Lock()
		if s.liveSessionsLocked(conn.AppID) >= maxSessionsPerConn {
			s.srvMu.Unlock()
			http.Error(w, "too many open MCP sessions for this connection; close one or wait for idle sessions to expire", http.StatusTooManyRequests)
			return
		}
		cs := s.srvByApp[conn.AppID]
		cs.starting++
		s.srvMu.Unlock()
		defer func() {
			s.srvMu.Lock()
			if cur := s.srvByApp[conn.AppID]; cur != nil {
				cur.starting--
			}
			s.srvMu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
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
		if errors.Is(err, ErrHubLocked) {
			w.Header().Set("Retry-After", "30")
			http.Error(w, "the hub is locked; unlock it in the Lokihub app", http.StatusServiceUnavailable)
			return
		}
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
	h := echo.WrapHandler(s.handler(true))
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
