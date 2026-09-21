package wails

import (
	"encoding/json"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartupErrorRoutes(t *testing.T) {
	e := echo.New()
	registerStartupErrorRoutes(errors.New("SQL logic error: no such column: parent_kind (1)"))(e)

	for _, route := range []string{"/api/info", "/api/apps", "/api/setup/status"} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(nethttp.MethodGet, route, nil))

		assert.Equal(t, nethttp.StatusServiceUnavailable, rec.Code, route)

		var body startupErrorResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), route)
		assert.Equal(t, StartupErrorCode, body.Code, route)
		assert.Equal(t, "SQL logic error: no such column: parent_kind (1)", body.Message, route)
	}
}
