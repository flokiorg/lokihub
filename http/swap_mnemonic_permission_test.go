package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/tests/db"
	"github.com/flokiorg/lokihub/tests/mocks"
)

// A readonly token must not be able to extract the swap seed.
//
// "readonly" exists to be the tier that cannot spend, and the swap mnemonic is
// a BIP39 seed controlling swap funds — anyone holding it can sweep them, with
// no further help from this hub. Handing it out on a read-only credential
// makes that tier meaningless for the one thing it is for.
//
// This codebase already sets the standard one route over: the node's own
// mnemonic is on the full-access group AND makes the caller re-enter the
// unlock password. The swap seed being readable with neither is an oversight,
// not a different policy.
func TestSwapMnemonic_ReadonlyTokenIsRefused(t *testing.T) {
	e := echo.New()
	logger.Init(strconv.Itoa(int(4)))
	mockSvc := mocks.NewMockService(t)
	gormDb, err := db.NewDB(t)
	require.NoError(t, err)
	defer db.CloseDB(gormDb)

	mockConfig := mocks.NewMockConfig(t)
	mockConfig.On("GetEnv").Return(&config.AppConfig{})
	mockConfig.On("CheckUnlockPassword", "123").Return(true)
	mockConfig.On("GetJWTSecret").Return("dummy secret", nil)

	mockSvc.On("GetDB").Return(gormDb)
	mockSvc.On("GetConfig").Return(mockConfig)
	mockKeys := mocks.NewMockKeys(t)
	// Returned only if the handler is actually reached — which is the bug.
	mockKeys.On("GetSwapMnemonic").Return("abandon abandon abandon").Maybe()
	mockSvc.On("GetKeys").Return(mockKeys)
	mockSvc.On("GetLokiSvc").Return(mocks.NewMockLokiService(t))
	mockSvc.On("GetAppStoreSvc").Return(&mocks.MockAppStoreService{})

	httpSvc := NewHttpService(mockSvc, events.NewEventPublisher())
	httpSvc.RegisterSharedRoutes(e)

	jsonBody, _ := json.Marshal(api.UnlockRequest{UnlockPassword: "123", Permission: "readonly"})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/unlock", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	var unlocked struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(body, &unlocked))
	require.NotEmpty(t, unlocked.Token)

	req2 := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/swaps/mnemonic", nil)
	req2.Header.Set("Authorization", "Bearer "+unlocked.Token)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	assert.Equal(t, http.StatusForbidden, rec2.Code,
		"a readonly token must be refused the swap seed — it is spending material, and readonly is the tier that cannot spend")
}
