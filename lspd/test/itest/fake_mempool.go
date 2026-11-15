package itest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
)

// FakeMempoolServer stands in for a real mempool.space-style fee API
// (internal/adapters/mempool.MempoolClient's only dependency: a single
// GET {base}/fees/recommended endpoint) so a scenario can simulate fee
// congestion -- or any other fee condition -- without needing a real
// external service. Every flspd process under test points at one of
// these via MEMPOOL_API_BASE_URL; SetFees lets a scenario step change
// what it reports mid-run, take effect on flspd's very next fee query
// (there's no caching on either side).
type FakeMempoolServer struct {
	server *httptest.Server

	mu   sync.Mutex
	fees recommendedFees
}

type recommendedFees struct {
	FastestFee  float64 `json:"fastestFee"`
	HalfHourFee float64 `json:"halfHourFee"`
	HourFee     float64 `json:"hourFee"`
	EconomyFee  float64 `json:"economyFee"`
	MinimumFee  float64 `json:"minimumFee"`
}

// NewFakeMempoolServer starts the fake server with a low, uncongested
// baseline (1 loki/vByte across every tier) -- a scenario only needs to
// call SetFees when it actually wants to exercise a congestion condition.
func NewFakeMempoolServer() *FakeMempoolServer {
	f := &FakeMempoolServer{
		fees: recommendedFees{
			FastestFee:  1,
			HalfHourFee: 1,
			HourFee:     1,
			EconomyFee:  1,
			MinimumFee:  1,
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/fees/recommended", f.handleFeesRecommended)
	f.server = httptest.NewServer(mux)
	return f
}

func (f *FakeMempoolServer) handleFeesRecommended(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	fees := f.fees
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(fees)
}

// SetFees replaces every tier at once (matching the real API's shape --
// there's no partial-update concept), so a scenario step always sets a
// complete, unambiguous fee schedule rather than leaving some tiers at
// whatever they happened to be before.
func (f *FakeMempoolServer) SetFees(fastest, halfHour, hour, economy, minimum float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fees = recommendedFees{
		FastestFee:  fastest,
		HalfHourFee: halfHour,
		HourFee:     hour,
		EconomyFee:  economy,
		MinimumFee:  minimum,
	}
}

// URL is the base URL to hand to MEMPOOL_API_BASE_URL.
func (f *FakeMempoolServer) URL() string {
	return f.server.URL
}

func (f *FakeMempoolServer) Close() {
	f.server.Close()
}
