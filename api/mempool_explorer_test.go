package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests/mocks"
)

// With no explorer configured nothing is asked of any explorer; a configured
// one is asked, at its own address.
func TestRequestMempoolApi_AsksOnlyTheConfiguredExplorer(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		assert.Equal(t, "/api/address/fc1qtest/txs", r.URL.Path)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cfg := mocks.NewMockConfig(t)
	theAPI := &api{cfg: cfg}
	cfg.On("GetMempoolApi").Return("").Once()
	_, err := theAPI.RequestMempoolApi(context.TODO(), "/address/fc1qtest/txs")
	require.ErrorIs(t, err, ErrNoExplorer)
	assert.Zero(t, hits.Load())

	cfg.On("GetMempoolApi").Return(srv.URL).Once()
	got, err := theAPI.RequestMempoolApi(context.TODO(), "/address/fc1qtest/txs")
	require.NoError(t, err)
	assert.Equal(t, []interface{}{}, got)
	assert.EqualValues(t, 1, hits.Load())
}
