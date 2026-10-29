package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/lsps/manager"
)

// A purchase refused for live terms worse than announced answers 409 with
// both values, so the order screen can ask again; any other purchase error
// stays a 500 with its message only.
func TestPurchaseTermsRefusalIsAConflictWithTheTerms(t *testing.T) {
	terms := manager.Terms{Worse: true, Fields: []manager.TermField{
		{Field: "lsps2_fee[0].min_fee", Announced: "2000000", Live: "3000000", Verdict: "worse"}}}
	e := echo.New()

	rec := httptest.NewRecorder()
	err := handleErrorWithTimeout(e.NewContext(httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil), rec),
		fmt.Errorf("buy: %w", &manager.TermsWorseError{Terms: terms}))
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, rec.Code)
	var body TermsErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, terms, body.Terms)
	assert.Contains(t, body.Message, "worse than it announced")

	rec = httptest.NewRecorder()
	require.NoError(t, handleErrorWithTimeout(e.NewContext(httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil), rec),
		errors.New("LSP returned error: no")))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "terms")
}
