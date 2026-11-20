package mcp

import (
	"context"
	"errors"
)

// Conn is an authenticated MCP connection — one AI-assistant App row and the
// capability scopes it was granted. It is resolved fresh from the bearer token
// on every request (see transport.go), which is what makes revocation and
// expiry take effect per-call: the owner JWT revocation denylist does not apply
// to MCP tokens, so the gate cannot assume a once-validated session stays valid.
type Conn struct {
	AppID   uint
	Name    string
	Scopes  []string // the mcp:* scopes granted to this connection
	TokenID string   // opaque id for audit / revocation
}

// Allows reports whether this connection may run the tool backing the given
// api.API method, applying the scope gate.
func (c *Conn) Allows(method string) error {
	cl, ok := classify(method)
	if !ok {
		return ErrNotATool
	}
	return Gate(c.Scopes, cl)
}

// Authenticator resolves a bearer token to a live connection, enforcing
// existence, expiry and revocation. It is called on every request, so a
// revoked or expired token stops working on the next tool call.
type Authenticator interface {
	// Resolve validates the bearer credential and returns the connection it
	// authenticates. It returns a non-nil error (one of the Err* below) for a
	// missing, unknown, expired or revoked token.
	Resolve(ctx context.Context, bearer string) (*Conn, error)
	// Revalidate re-checks an already-resolved connection — the App still
	// exists, is not expired, and is not revoked — and returns its CURRENT
	// scopes. Tool handlers call it on every invocation, so revocation and
	// re-scoping take effect on the next tool call, independent of session
	// lifetime.
	Revalidate(ctx context.Context, c *Conn) ([]string, error)
}

var (
	// ErrNoToken — no bearer credential was presented.
	ErrNoToken = errors.New("mcp: no bearer token")
	// ErrInvalidToken — the token is malformed or unknown.
	ErrInvalidToken = errors.New("mcp: invalid token")
	// ErrExpired — the connection has passed its expiry.
	ErrExpired = errors.New("mcp: connection expired")
	// ErrRevoked — the connection was revoked by the owner.
	ErrRevoked = errors.New("mcp: connection revoked")
	// ErrUnauthenticated — a tool handler ran without a connection in context
	// (a wiring bug; the transport must always authenticate first).
	ErrUnauthenticated = errors.New("mcp: unauthenticated")
)

type connCtxKey struct{}

// withConn returns a context carrying the authenticated connection.
func withConn(ctx context.Context, c *Conn) context.Context {
	return context.WithValue(ctx, connCtxKey{}, c)
}

// ConnFrom returns the authenticated connection from the context, if any.
func ConnFrom(ctx context.Context) (*Conn, bool) {
	c, ok := ctx.Value(connCtxKey{}).(*Conn)
	return c, ok
}
