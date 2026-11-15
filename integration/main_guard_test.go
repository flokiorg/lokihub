//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The regtest guard refuses a Hub it cannot prove is on regtest, and leaves a
// config with no admin API (every test skips) alone.
func TestRegtestGuard(t *testing.T) {
	if err := regtestGuard(&Config{}); err != nil {
		t.Fatalf("no admin API: nothing to check, got %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not a regtest hub", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := regtestGuard(&Config{AdminAPI: AdminAPIConfig{BaseURL: srv.URL, Token: "t"}}); err == nil {
		t.Fatal("a Hub that does not answer get_info on regtest must be refused")
	}
}
