//go:build testhooks

package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/tests/db"
	"github.com/flokiorg/lokihub/tests/mocks"
)

// hooksServer is an HTTP service whose node reports network, or no node when
// network is "".
func hooksServer(t *testing.T, network string) (*echo.Echo, string) {
	t.Helper()
	logger.Init("4")
	e := echo.New()
	gormDb, err := db.NewDB(t)
	require.NoError(t, err)
	t.Cleanup(func() { db.CloseDB(gormDb) })

	mockConfig := mocks.NewMockConfig(t)
	mockConfig.On("GetEnv").Return(&config.AppConfig{}).Maybe()
	mockConfig.On("CheckUnlockPassword", "123").Return(true).Maybe()
	mockConfig.On("GetJWTSecret").Return("dummy secret", nil).Maybe()

	mockSvc := mocks.NewMockService(t)
	mockSvc.On("GetDB").Return(gormDb)
	mockSvc.On("GetConfig").Return(mockConfig)
	mockSvc.On("GetKeys").Return(mocks.NewMockKeys(t))
	mockSvc.On("GetHubInfoSvc").Return(mocks.NewMockHubInfoService(t))
	mockSvc.On("GetAppStoreSvc").Return(&mocks.MockAppStoreService{}).Maybe()
	if network == "" {
		mockSvc.On("GetLNClient").Return(nil).Maybe()
	} else {
		ln := mocks.NewMockLNClient(t)
		ln.On("GetInfo", mock.Anything).Return(&lnclient.NodeInfo{Network: network}, nil).Maybe()
		mockSvc.On("GetLNClient").Return(ln).Maybe()
	}

	stubInfoCaches(mockSvc)
	httpSvc := NewHttpService(mockSvc, events.NewEventPublisher())
	httpSvc.RegisterSharedRoutes(e)

	body, _ := json.Marshal(api.UnlockRequest{UnlockPassword: "123", Permission: "full"})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/unlock", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var out struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return e, out.Token
}

func hookStatus(t *testing.T, e *echo.Echo, token string) int {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/test/chain/withhold",
		bytes.NewBufferString(`{"txid":"00","on":true}`))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code
}

// The hooks exist only with LOKIHUB_TEST_HOOKS=1: without it the path is not a hook
// (the regtest gate, which answers 403 beside this mainnet node, never runs).
func TestTestHooks_OffWithoutEnv(t *testing.T) {
	t.Setenv(TestHooksEnv, "")
	e, token := hooksServer(t, "bitcoin")
	require.NotEqual(t, http.StatusForbidden, hookStatus(t, e, token))

	t.Setenv(TestHooksEnv, "1")
	e, token = hooksServer(t, "bitcoin")
	require.Equal(t, http.StatusForbidden, hookStatus(t, e, token))
}

// Every hook needs the admin token and a regtest node beside it.
func TestTestHooks_Gates(t *testing.T) {
	t.Setenv(TestHooksEnv, "1")

	e, _ := hooksServer(t, "regtest")
	require.Equal(t, http.StatusBadRequest, hookStatus(t, e, ""), "no token")
	require.Equal(t, http.StatusUnauthorized, hookStatus(t, e, "not-a-jwt"), "a bad token")

	for _, network := range []string{"bitcoin", "testnet", "signet"} {
		e, token := hooksServer(t, network)
		require.Equal(t, http.StatusForbidden, hookStatus(t, e, token), network)
	}

	e, token := hooksServer(t, "")
	require.Equal(t, http.StatusServiceUnavailable, hookStatus(t, e, token), "no node started")
}
