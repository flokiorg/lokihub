package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
	testdb "github.com/flokiorg/lokihub/tests/db"
	"github.com/flokiorg/lokihub/tests/mocks"
)

// newTokenTestService wires an HttpService against an in-memory DB and a mock
// config whose JWT secret is fixed, so a token minted by httpSvc.createJWT
// validates against the same secret in the auth middleware.
func newTokenTestService(t *testing.T) (*echo.Echo, *HttpService) {
	t.Helper()
	e := echo.New()
	logger.Init(strconv.Itoa(int(4)))
	mockSvc := mocks.NewMockService(t)
	gormDb, err := testdb.NewDB(t)
	require.NoError(t, err)
	t.Cleanup(func() { testdb.CloseDB(gormDb) })

	mockConfig := mocks.NewMockConfig(t)
	mockConfig.On("GetEnv").Return(&config.AppConfig{})
	mockConfig.On("GetJWTSecret").Return("dummy secret", nil)

	mockSvc.On("GetDB").Return(gormDb)
	mockSvc.On("GetConfig").Return(mockConfig)
	mockSvc.On("GetKeys").Return(mocks.NewMockKeys(t))
	mockSvc.On("GetLokiSvc").Return(mocks.NewMockHubInfoService(t))
	mockSvc.On("GetAppStoreSvc").Return(&mocks.MockAppStoreService{})

	httpSvc := NewHttpService(mockSvc, events.NewEventPublisher())
	httpSvc.RegisterSharedRoutes(e)
	return e, httpSvc
}

// mintToken signs a token of the given permission directly, bypassing the
// rate-limited /api/unlock endpoint.
func mintToken(t *testing.T, httpSvc *HttpService, permission string) string {
	t.Helper()
	token, err := httpSvc.createJWT(nil, permission)
	require.NoError(t, err)
	require.NotEmpty(t, token)
	return token
}

func TestRedactTokenQueryParam(t *testing.T) {
	cases := map[string]string{
		"/api/lsps5/events?token=secret":           "/api/lsps5/events?token=[REDACTED]",
		"/api/circle/avatar-proxy?url=x&token=abc": "/api/circle/avatar-proxy?url=x&token=[REDACTED]",
		"/api/lsps5/events?token=abc&foo=bar":      "/api/lsps5/events?token=[REDACTED]&foo=bar",
		"/api/transactions":                        "/api/transactions",
		"/api/x?other=1":                           "/api/x?other=1",
	}
	for in, want := range cases {
		assert.Equal(t, want, redactTokenQueryParam(in), in)
	}
}

// A fund-moving (full-access) route must not accept the token in the query
// string — only the avatar-proxy and SSE routes keep that fallback. A query
// token lands in the request logger and nwc.log, so the rest of the API is
// header-only.
func TestFullAccessRoute_RejectsQueryToken(t *testing.T) {
	e, httpSvc := newTokenTestService(t)
	full := mintToken(t, httpSvc, "full")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/identity-authorities?token="+url.QueryEscape(full), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// echojwt reports a token it could not extract as 400 "missing or
	// malformed jwt" (a 401 would mean a token WAS extracted but failed to
	// parse) — so this confirms the query token was never even looked at on a
	// header-only route.
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"a full-access route must reject a token passed in the query string")
	assert.Contains(t, rec.Body.String(), "missing or malformed jwt")
}

// The app log is the nwc.log tail; a read-only token must not be able to read
// it (it can hold node internals and, historically, leaked bearer tokens).
func TestLogApp_RequiresFullAccess(t *testing.T) {
	e, httpSvc := newTokenTestService(t)

	readonly := mintToken(t, httpSvc, "readonly")
	reqRO := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/log/app", nil)
	reqRO.Header.Set("Authorization", "Bearer "+readonly)
	recRO := httptest.NewRecorder()
	e.ServeHTTP(recRO, reqRO)
	assert.Equal(t, http.StatusForbidden, recRO.Code,
		"a read-only token must be refused the app log")

	full := mintToken(t, httpSvc, "full")
	reqF := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/log/app", nil)
	reqF.Header.Set("Authorization", "Bearer "+full)
	recF := httptest.NewRecorder()
	e.ServeHTTP(recF, reqF)
	assert.Equal(t, http.StatusOK, recF.Code,
		"a full-access token may read the app log")
}

// The avatar-proxy carve-out: an <img> cannot set an Authorization header, so
// this read-only route still accepts the query token. Reaching the handler
// (here a 400 for the missing url param) proves auth passed; with no token at
// all the request is rejected.
func TestAvatarProxy_AcceptsQueryTokenCarveOut(t *testing.T) {
	e, httpSvc := newTokenTestService(t)
	readonly := mintToken(t, httpSvc, "readonly")

	// With a valid query token but no url param, the handler is reached and
	// returns its own "url is required" 400 — distinct from echojwt's "missing
	// or malformed jwt", which is what a rejected request would carry. The
	// body message is what proves auth passed.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/circle/avatar-proxy?token="+url.QueryEscape(readonly), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "url is required",
		"the avatar proxy must accept a query token and reach the handler")

	reqNoAuth := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/circle/avatar-proxy?url=x", nil)
	recNoAuth := httptest.NewRecorder()
	e.ServeHTTP(recNoAuth, reqNoAuth)
	assert.Equal(t, http.StatusBadRequest, recNoAuth.Code)
	assert.Contains(t, recNoAuth.Body.String(), "missing or malformed jwt",
		"the avatar proxy still requires a token")
}

// The SSE carve-out: an EventSource cannot set an Authorization header, so the
// /api/lsps5/events route keeps the query token. A cancelled request context
// makes the handler return promptly. A read-only token is still refused (full
// access required), and no token is rejected.
func TestSSEEvents_AcceptsQueryTokenCarveOut(t *testing.T) {
	e, httpSvc := newTokenTestService(t)
	full := mintToken(t, httpSvc, "full")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet,
		"/api/lsps5/events?token="+url.QueryEscape(full), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code,
		"the SSE route must accept a full-access query token")

	readonly := mintToken(t, httpSvc, "readonly")
	ctxRO, cancelRO := context.WithCancel(t.Context())
	cancelRO()
	reqRO := httptest.NewRequestWithContext(ctxRO, http.MethodGet,
		"/api/lsps5/events?token="+url.QueryEscape(readonly), nil)
	recRO := httptest.NewRecorder()
	e.ServeHTTP(recRO, reqRO)
	assert.Equal(t, http.StatusForbidden, recRO.Code,
		"the SSE route requires full access even with a valid read-only query token")

	reqNoAuth := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/lsps5/events", nil)
	recNoAuth := httptest.NewRecorder()
	e.ServeHTTP(recNoAuth, reqNoAuth)
	assert.Equal(t, http.StatusBadRequest, recNoAuth.Code,
		"the SSE route still requires a token")
	assert.Contains(t, recNoAuth.Body.String(), "missing or malformed jwt")
}
