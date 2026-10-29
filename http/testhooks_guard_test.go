//go:build testhooks

package http

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/testknobs"
	"github.com/flokiorg/lokihub/tests/db"
	"github.com/flokiorg/lokihub/tests/mocks"
)

// hookServer is the HTTP API of a testhooks build whose node reports network
// (none when network is ""), with a full and a readonly token.
type hookServer struct {
	e              *echo.Echo
	full, readonly string
}

func newHookServer(t *testing.T, network string) *hookServer {
	t.Helper()
	return newHookServerSwitch(t, network, "1")
}

// newHookServerSwitch is newHookServer with LOKIHUB_TEST_HOOKS set to hooks.
func newHookServerSwitch(t *testing.T, network, hooks string) *hookServer {
	t.Helper()
	t.Setenv("LOKIHUB_TEST_HOOKS", hooks)
	t.Cleanup(func() { testknobs.SetFaults(testknobs.Faults{}, true); testknobs.SetLimits(testknobs.Limits{}, true) })
	logger.Init(strconv.Itoa(4))
	e := echo.New()
	mockSvc := mocks.NewMockService(t)
	gormDb, err := db.NewDB(t)
	require.NoError(t, err)
	t.Cleanup(func() { db.CloseDB(gormDb) })

	mockConfig := mocks.NewMockConfig(t)
	mockConfig.On("GetEnv").Return(&config.AppConfig{}).Maybe()
	mockConfig.On("GetJWTSecret").Return("dummy secret", nil).Maybe()
	mockSvc.On("GetDB").Return(gormDb).Maybe()
	mockSvc.On("GetConfig").Return(mockConfig).Maybe()
	mockSvc.On("GetKeys").Return(mocks.NewMockKeys(t)).Maybe()
	mockSvc.On("GetLokiSvc").Return(mocks.NewMockLokiService(t)).Maybe()
	mockSvc.On("GetAppStoreSvc").Return(&mocks.MockAppStoreService{}).Maybe()
	if network == "" {
		mockSvc.On("GetLNClient").Return(nil).Maybe()
	} else {
		ln := mocks.NewMockLNClient(t)
		ln.On("GetInfo", mock.Anything).Return(&lnclient.NodeInfo{Network: network}, nil).Maybe()
		mockSvc.On("GetLNClient").Return(ln).Maybe()
	}

	httpSvc := NewHttpService(mockSvc, events.NewEventPublisher())
	httpSvc.RegisterSharedRoutes(e)
	full, err := httpSvc.createJWT(nil, "full")
	require.NoError(t, err)
	readonly, err := httpSvc.createJWT(nil, "readonly")
	require.NoError(t, err)
	return &hookServer{e: e, full: full, readonly: readonly}
}

func (s *hookServer) call(t *testing.T, method, path, token string) int {
	t.Helper()
	return s.callWith(t, method, path, token, `{}`)
}

func (s *hookServer) callWith(t *testing.T, method, path, token, body string) int {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	return rec.Code
}

// hookRoutes is every route the build mounts under /api/test.
func (s *hookServer) hookRoutes() []*echo.Route {
	var out []*echo.Route
	for _, r := range s.e.Routes() {
		// Not echo's catch-all of the group: only the routes it serves.
		if strings.HasPrefix(r.Path, "/api/test/") && !strings.HasSuffix(r.Path, "*") {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// I-76: every test hook refuses a caller without the admin token, with a
// readonly token, and on a node that is not on regtest or not started; with the
// full token on a regtest node each one is served. A hook added later is
// covered by the same walk over the mounted routes.
func TestTestHooks_EveryHookNeedsTheAdminTokenAndARegtestNode(t *testing.T) {
	regtest := newHookServer(t, "regtest")
	routes := regtest.hookRoutes()
	paths := make([]string, len(routes))
	for i, r := range routes {
		paths[i] = r.Path
	}
	assert.Subset(t, paths, []string{"/api/test/withdraw/hooks", "/api/test/clock", "/api/test/faults",
		"/api/test/cash/limits", "/api/test/cash/drop-archive"}, "the review-3 hooks are mounted")

	mainnet := newHookServer(t, "mainnet")
	testnet := newHookServer(t, "testnet")
	stopped := newHookServer(t, "")
	for _, r := range routes {
		t.Run(r.Method+" "+r.Path, func(t *testing.T) {
			// echo-jwt answers a missing token 400, a bad one 401.
			assert.Equal(t, http.StatusBadRequest, regtest.call(t, r.Method, r.Path, ""), "no token")
			assert.Equal(t, http.StatusUnauthorized, regtest.call(t, r.Method, r.Path, "not-a-token"), "a forged token")
			assert.Equal(t, http.StatusForbidden, regtest.call(t, r.Method, r.Path, regtest.readonly), "a readonly token")
			assert.Equal(t, http.StatusForbidden, mainnet.call(t, r.Method, r.Path, mainnet.full), "a mainnet node")
			assert.Equal(t, http.StatusForbidden, testnet.call(t, r.Method, r.Path, testnet.full), "a testnet node")
			assert.Equal(t, http.StatusServiceUnavailable, stopped.call(t, r.Method, r.Path, stopped.full), "no node")

			code := regtest.call(t, r.Method, r.Path, regtest.full)
			assert.NotContains(t, []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}, code,
				"served with the full token on regtest (got %d)", code)
		})
	}
}

// A refused hook call changes nothing: the fault switch stays off until the
// full token on a regtest node turns it on.
func TestTestHooks_RefusedCallsReachNoHandler(t *testing.T) {
	regtest := newHookServer(t, "regtest")
	mainnet := newHookServer(t, "mainnet")
	body := `{"break_signer": true}`
	for _, c := range []struct {
		s     *hookServer
		token string
	}{{regtest, ""}, {regtest, regtest.readonly}, {mainnet, mainnet.full}} {
		c.s.callWith(t, http.MethodPost, "/api/test/faults", c.token, body)
		assert.False(t, testknobs.SignerBroken())
	}
	assert.Equal(t, http.StatusOK, regtest.callWith(t, http.MethodPost, "/api/test/faults", regtest.full, body))
	assert.True(t, testknobs.SignerBroken())
}

// Without LOKIHUB_TEST_HOOKS=1 a testhooks build mounts no hook at all: a
// hook path reaches no handler, whatever the token and network.
func TestTestHooks_NoneMountedWithoutTheSwitch(t *testing.T) {
	off := newHookServerSwitch(t, "regtest", "0")
	assert.Empty(t, off.hookRoutes())
	off.callWith(t, http.MethodPost, "/api/test/faults", off.full, `{"break_signer": true}`)
	assert.False(t, testknobs.SignerBroken())
}
