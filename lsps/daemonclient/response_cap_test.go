package daemonclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testNsec is a deterministic 32-byte hex key; the test server does not verify
// the NIP-98 Authorization, so any valid signer key will do.
const testNsec = "0000000000000000000000000000000000000000000000000000000000000001"

// TestResponseBody_OversizedIsRejected is the regression test for LOKIHUB-0044:
// a compromised/MITM'd lspd that returns a multi-GB 200 must not be buffered in
// full. Both decode sites — the JSON-RPC success path (call) and the public GET
// path (getJSON) — are now bounded by io.LimitReader(maxResponseBytes), so an
// oversized body is rejected mid-stream instead of allocated whole. The bodies
// below are well-formed JSON that would decode cleanly if read in full; the only
// reason they fail is the cap, which is exactly the property under test.
func TestResponseBody_OversizedIsRejected(t *testing.T) {
	// A single JSON string value larger than the cap, so truncating it at the
	// cap cuts the surrounding JSON and makes decoding fail.
	oversized := strings.Repeat("a", maxResponseBytes+(1<<20))

	t.Run("call (dashboard JSON-RPC success path)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":"` + oversized + `","error":null}`))
		}))
		defer srv.Close()

		c := New(Config{Endpoint: srv.URL, Nsec: testNsec})
		if _, err := c.GetSummary(context.Background()); err == nil {
			t.Fatal("expected an error for an oversized dashboard response, got nil (body was buffered unbounded)")
		}
	})

	t.Run("getJSON (public /info path)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"` + oversized + `"}`))
		}))
		defer srv.Close()

		c := New(Config{Endpoint: srv.URL, Nsec: testNsec})
		if _, err := c.Info(context.Background()); err == nil {
			t.Fatal("expected an error for an oversized /info response, got nil (body was buffered unbounded)")
		}
	})
}

// TestResponseBody_NormalIsAccepted guards against the cap breaking normal
// operation: a small, well-formed response under the cap must still decode.
func TestResponseBody_NormalIsAccepted(t *testing.T) {
	t.Run("call", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":{"ok":true},"error":null}`))
		}))
		defer srv.Close()

		c := New(Config{Endpoint: srv.URL, Nsec: testNsec})
		raw, err := c.GetSummary(context.Background())
		if err != nil {
			t.Fatalf("a normal dashboard response must decode: %v", err)
		}
		if len(raw) == 0 {
			t.Fatal("expected a non-empty result")
		}
	})

	t.Run("getJSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"1.2.3","dashboard_enabled":true}`))
		}))
		defer srv.Close()

		c := New(Config{Endpoint: srv.URL, Nsec: testNsec})
		info, err := c.Info(context.Background())
		if err != nil {
			t.Fatalf("a normal /info response must decode: %v", err)
		}
		if info.Version != "1.2.3" {
			t.Fatalf("version = %q, want 1.2.3", info.Version)
		}
	})
}
