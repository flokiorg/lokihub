package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseExpiry reads the ExpiresAt of a token signed with the test secret.
func parseExpiry(t *testing.T, token string) time.Time {
	t.Helper()
	claims := &jwtCustomClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(_ *jwt.Token) (interface{}, error) {
		return []byte("dummy secret"), nil
	})
	require.NoError(t, err)
	require.NotNil(t, claims.ExpiresAt)
	return claims.ExpiresAt.Time
}

// Logout must invalidate the presented token server-side: a stateless HS256
// JWT otherwise keeps working until its ExpiresAt. After POST /api/logout the
// same token is rejected on every group.
func TestLogout_InvalidatesToken(t *testing.T) {
	e, httpSvc := newTokenTestService(t)
	full := mintToken(t, httpSvc, "full")

	// The token works before logout (a full-access route returns 200).
	reqBefore := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/log/app", nil)
	reqBefore.Header.Set("Authorization", "Bearer "+full)
	recBefore := httptest.NewRecorder()
	e.ServeHTTP(recBefore, reqBefore)
	require.Equal(t, http.StatusOK, recBefore.Code,
		"the token must be accepted before logout")

	// Log out.
	reqLogout := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/logout", nil)
	reqLogout.Header.Set("Authorization", "Bearer "+full)
	recLogout := httptest.NewRecorder()
	e.ServeHTTP(recLogout, reqLogout)
	require.Equal(t, http.StatusNoContent, recLogout.Code)

	// The same token is now rejected.
	reqAfter := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/log/app", nil)
	reqAfter.Header.Set("Authorization", "Bearer "+full)
	recAfter := httptest.NewRecorder()
	e.ServeHTTP(recAfter, reqAfter)
	assert.Equal(t, http.StatusUnauthorized, recAfter.Code,
		"the token must be rejected after logout")
	assert.Contains(t, recAfter.Body.String(), "revoked")
}

// A revoked token is rejected on the read-only group too, not only on the
// full-access routes.
func TestRevokedToken_RejectedOnReadOnlyGroup(t *testing.T) {
	e, httpSvc := newTokenTestService(t)
	readonly := mintToken(t, httpSvc, "readonly")

	reqLogout := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/logout", nil)
	reqLogout.Header.Set("Authorization", "Bearer "+readonly)
	recLogout := httptest.NewRecorder()
	e.ServeHTTP(recLogout, reqLogout)
	require.Equal(t, http.StatusNoContent, recLogout.Code)

	// A read-only route rejects the revoked token at the revocation middleware,
	// before its handler runs.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/balances", nil)
	req.Header.Set("Authorization", "Bearer "+readonly)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"a revoked read-only token must be rejected on the read-only group")
	assert.Contains(t, rec.Body.String(), "revoked")
}

// A caller-supplied lifetime is capped so a fund-moving token cannot be minted
// effectively permanent, and the default is the shortened bound.
func TestTokenLifetime_Capped(t *testing.T) {
	_, httpSvc := newTokenTestService(t)

	huge := uint64(100 * 365)
	capped, err := httpSvc.createJWT(&huge, "full")
	require.NoError(t, err)
	exp := parseExpiry(t, capped)
	assert.True(t, exp.Before(time.Now().Add((maxTokenExpiryDays+2)*24*time.Hour)),
		"an oversized tokenExpiryDays must be capped at maxTokenExpiryDays")
	assert.True(t, exp.After(time.Now().Add((maxTokenExpiryDays-2)*24*time.Hour)),
		"the capped lifetime should still be close to maxTokenExpiryDays")

	def, err := httpSvc.createJWT(nil, "full")
	require.NoError(t, err)
	expDef := parseExpiry(t, def)
	assert.True(t, expDef.Before(time.Now().Add((defaultTokenExpiryDays+2)*24*time.Hour)),
		"the default lifetime must be the shortened default, not a month or a century")
}

// Every minted token carries a unique jti, which is what makes single-token
// revocation possible.
func TestMintedToken_HasUniqueJTI(t *testing.T) {
	_, httpSvc := newTokenTestService(t)

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		token := mintToken(t, httpSvc, "full")
		claims := &jwtCustomClaims{}
		_, err := jwt.ParseWithClaims(token, claims, func(_ *jwt.Token) (interface{}, error) {
			return []byte("dummy secret"), nil
		})
		require.NoError(t, err)
		require.NotEmpty(t, claims.ID, "every token must carry a jti")
		require.False(t, seen[claims.ID], "jti must be unique per token")
		seen[claims.ID] = true
	}
}
