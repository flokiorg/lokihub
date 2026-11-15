package hubinfo

import (
	"context"

	"github.com/flokiorg/lokihub/internal/seed"
)

type HubInfoService interface {
	GetInfo(ctx context.Context) (*HubInfo, error)
	GetFiatRate(ctx context.Context) (*FiatRate, error)
	GetCurrencies(ctx context.Context) (map[string]FiatCurrency, error)
	GetFAQ(ctx context.Context) ([]FAQ, error)
}

// RateOracle supplies the latest Flokicoin fiat rate fetched from the NIP-78
// oracle (see oracle.go). GetFiatRate reads through it. Latest
// returns an error when no rate has been fetched yet or the last-good one is
// stale, which the UI degrades to sats/FLC on.
type RateOracle interface {
	Latest() (*FiatRate, error)
}

type FAQ struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type FiatCurrency struct {
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
}

type HubInfo struct {
	Version      string `json:"version"`
	ReleaseNotes string `json:"releaseNotes"` // Markdown format
	// ImageProxyMode / ImageProxyTemplate expose the profile-image gating choice
	// (P0-B) so the frontend knows whether and how to load external profile
	// images. Mode is one of "off" (default; show initials, no banner),
	// "direct" (load source URL directly) or "proxy" (route the source URL
	// through ImageProxyTemplate). The template contains one {url} or {b64url}
	// placeholder and is only meaningful in "proxy" mode.
	ImageProxyMode     string `json:"imageProxyMode"`
	ImageProxyTemplate string `json:"imageProxyTemplate"`
	// NativeAsset is the hub's on-chain asset descriptor (ticker/units/decimals/
	// explorer) so the frontend formats amounts and builds explorer links from
	// one source of truth. Embedded default = Flokicoin.
	NativeAsset seed.NativeAsset `json:"nativeAsset"`
}

type FiatRate struct {
	Code      string  `json:"code"`
	Symbol    string  `json:"symbol"`
	Rate      string  `json:"rate"`
	RateFloat float64 `json:"rate_float"`
}

type ErrorResponse struct {
	Message string `json:"message"`
}
