package mcp

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeLoader is a test AppLoader.
type fakeLoader struct {
	apps map[uint]AppInfo
	gone map[uint]bool // revoked / deleted
}

func (f *fakeLoader) LoadApp(_ context.Context, appID uint) (AppInfo, bool, error) {
	if f.gone[appID] {
		return AppInfo{}, false, nil
	}
	info, ok := f.apps[appID]
	if !ok {
		return AppInfo{}, false, nil
	}
	return info, true, nil
}

func fixedNow(ts time.Time) func() time.Time { return func() time.Time { return ts } }

func TestStandardAuthenticator(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	loader := &fakeLoader{
		apps: map[uint]AppInfo{
			1: {Name: "ok", Kind: AppKindMCP, Scopes: []string{GroupWallet.Scope(ModeRead)}},
			2: {Name: "expired", Kind: AppKindMCP, ExpiresAt: &past},
			3: {Name: "valid-exp", Kind: AppKindMCP, ExpiresAt: &future},
			4: {Name: "not-mcp", Kind: "standard"}, // a plain NWC app
		},
		gone: map[uint]bool{},
	}
	store := NewMemTokenStore(map[string]uint{
		"tok-ok":      1,
		"tok-expired": 2,
		"tok-valid":   3,
		"tok-notmcp":  4,
		"tok-missing": 9, // points at a non-existent app
	})
	a := &StandardAuthenticator{Store: store, Apps: loader, Now: fixedNow(now)}
	ctx := context.Background()

	tests := []struct {
		token   string
		wantErr error
	}{
		{"", ErrNoToken},
		{"unknown", ErrInvalidToken},
		{"tok-ok", nil},
		{"tok-valid", nil},
		{"tok-expired", ErrExpired},
		{"tok-notmcp", ErrInvalidToken}, // a non-MCP app id must not drive the MCP surface
		{"tok-missing", ErrRevoked},     // token maps to a deleted app
	}
	for _, tc := range tests {
		conn, err := a.Resolve(ctx, tc.token)
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("Resolve(%q) err = %v, want %v", tc.token, err, tc.wantErr)
		}
		if tc.wantErr == nil && (conn == nil || conn.AppID == 0) {
			t.Errorf("Resolve(%q) returned no conn", tc.token)
		}
	}
}

// TestRevalidateCatchesMidSessionRevoke: a connection valid at Resolve must be
// refused by Revalidate once its App is deleted (the per-call revocation path).
func TestRevalidateCatchesMidSessionRevoke(t *testing.T) {
	loader := &fakeLoader{
		apps: map[uint]AppInfo{1: {Name: "ok", Kind: AppKindMCP}},
		gone: map[uint]bool{},
	}
	a := &StandardAuthenticator{Store: NewMemTokenStore(map[string]uint{"t": 1}), Apps: loader}
	ctx := context.Background()

	conn, err := a.Resolve(ctx, "t")
	if err != nil {
		t.Fatalf("initial Resolve: %v", err)
	}
	if _, err := a.Revalidate(ctx, conn); err != nil {
		t.Fatalf("Revalidate before revoke: %v", err)
	}

	loader.gone[1] = true // owner revokes mid-session
	if _, err := a.Revalidate(ctx, conn); !errors.Is(err, ErrRevoked) {
		t.Errorf("Revalidate after revoke = %v, want ErrRevoked", err)
	}
}
