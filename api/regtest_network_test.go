package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/tests/mocks"
)

func TestFlndMacaroonPath(t *testing.T) {
	require.Equal(t, filepath.Join("/d", "data", "chain", "flokicoin", "main", "admin.macaroon"), flndMacaroonPath("/d", "flokicoin"))
	require.Equal(t, filepath.Join("/d", "data", "chain", "flokicoin", "main", "admin.macaroon"), flndMacaroonPath("/d", ""))
	require.Equal(t, filepath.Join("/d", "data", "chain", "flokicoin", "regtest", "admin.macaroon"), flndMacaroonPath("/d", "regtest"))
}

// On regtest no explorer exists, so the stored address is kept without a lookup:
// the config mock fails the test if GetMempoolApi is called.
func TestGetUnusedOnchainAddress_RegtestSkipsExplorer(t *testing.T) {
	svc := mocks.NewMockService(t)
	svc.On("GetLNClient").Return(mocks.NewMockLNClient(t))
	cfg := mocks.NewMockConfig(t)
	cfg.On("Get", config.OnchainAddressKey, "").Return("fcrt1qexample", nil)
	cfg.On("GetNetwork").Return("regtest")

	a := &api{svc: svc, cfg: cfg}
	addr, err := a.GetUnusedOnchainAddress(context.Background())
	require.NoError(t, err)
	require.Equal(t, "fcrt1qexample", addr)
}

func TestGetUnusedOnchainAddress_MainnetStillAsksExplorer(t *testing.T) {
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
		require.Equal(t, "/api/address/fc1qexample/txs", r.URL.Path)
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	svc := mocks.NewMockService(t)
	svc.On("GetLNClient").Return(mocks.NewMockLNClient(t))
	cfg := mocks.NewMockConfig(t)
	cfg.On("Get", config.OnchainAddressKey, "").Return("fc1qexample", nil)
	cfg.On("GetNetwork").Return("flokicoin")
	cfg.On("GetMempoolApi").Return(srv.URL)

	a := &api{svc: svc, cfg: cfg}
	addr, err := a.GetUnusedOnchainAddress(context.Background())
	require.NoError(t, err)
	require.Equal(t, "fc1qexample", addr)
	require.True(t, asked)
}
