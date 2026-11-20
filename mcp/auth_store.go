package mcp

import (
	"context"
	"time"
)

// AppKindMCP is the db.App.Kind value for an AI-assistant connection. Keeping
// MCP connections in their own kind lets the Connections UI filter them and
// keeps their mcp:* scopes out of the NWC surface.
const AppKindMCP = "mcp"

// AppInfo is the subset of an App the authenticator needs. It is loaded fresh on
// every call so revocation (the App row is gone) and expiry take effect at once.
type AppInfo struct {
	Name      string
	Scopes    []string
	Kind      string
	ExpiresAt *time.Time
}

// AppLoader loads an MCP App by id. found is false when the App no longer exists
// (revoked). The real implementation reads db/apps; tests use a fake.
type AppLoader interface {
	LoadApp(ctx context.Context, appID uint) (info AppInfo, found bool, err error)
}

// TokenStore maps a presented bearer token to the App id it authenticates. The
// real implementation stores a hash of the minted token; tests use a map.
type TokenStore interface {
	AppID(ctx context.Context, token string) (appID uint, found bool)
}

// StandardAuthenticator is the production Authenticator: a token resolves to an
// App id (TokenStore), and the App is loaded and validated (AppLoader) on every
// request and re-validated on every tool call.
type StandardAuthenticator struct {
	Store TokenStore
	Apps  AppLoader
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
	// OnUse, when set, is called after every successful Resolve (e.g. to keep
	// the App's last-used time fresh for the owner's Connections list). It must
	// be cheap/throttled; it never affects the auth decision.
	OnUse func(appID uint)
}

func (a *StandardAuthenticator) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Resolve validates the bearer token and returns the connection.
func (a *StandardAuthenticator) Resolve(ctx context.Context, bearer string) (*Conn, error) {
	if bearer == "" {
		return nil, ErrNoToken
	}
	appID, ok := a.Store.AppID(ctx, bearer)
	if !ok {
		if av, isAv := a.Store.(interface{ Available() bool }); isAv && !av.Available() {
			return nil, ErrHubLocked
		}
		return nil, ErrInvalidToken
	}
	info, err := a.load(ctx, appID)
	if err != nil {
		return nil, err
	}
	if a.OnUse != nil {
		a.OnUse(appID)
	}
	return &Conn{AppID: appID, Name: info.Name, Scopes: info.Scopes}, nil
}

// Revalidate re-checks the connection's App still exists and is not expired,
// and returns its current scopes.
func (a *StandardAuthenticator) Revalidate(ctx context.Context, c *Conn) ([]string, error) {
	info, err := a.load(ctx, c.AppID)
	if err != nil {
		return nil, err
	}
	return info.Scopes, nil
}

func (a *StandardAuthenticator) load(ctx context.Context, appID uint) (AppInfo, error) {
	info, found, err := a.Apps.LoadApp(ctx, appID)
	if err != nil {
		return AppInfo{}, err
	}
	if !found {
		return AppInfo{}, ErrRevoked
	}
	if info.Kind != AppKindMCP {
		// A non-MCP App id must never be drivable through the MCP surface.
		return AppInfo{}, ErrInvalidToken
	}
	if info.ExpiresAt != nil && !a.now().Before(*info.ExpiresAt) {
		return AppInfo{}, ErrExpired
	}
	return info, nil
}

// MemTokenStore is an in-memory TokenStore for the dev loopback listener and
// tests. The persistent, hashed store lands with the Connect-AI flow (M4).
type MemTokenStore struct {
	m map[string]uint
}

// NewMemTokenStore builds a token store from a token→appID map.
func NewMemTokenStore(tokens map[string]uint) *MemTokenStore {
	m := make(map[string]uint, len(tokens))
	for k, v := range tokens {
		m[k] = v
	}
	return &MemTokenStore{m: m}
}

func (s *MemTokenStore) AppID(_ context.Context, token string) (uint, bool) {
	id, ok := s.m[token]
	return id, ok
}
