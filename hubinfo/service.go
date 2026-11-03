package hubinfo

import (
	"context"
	"errors"

	"github.com/flokiorg/lokihub/appversion"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/seed"
)

type hubInfoService struct {
	cfg    config.Config
	oracle RateOracle
}

// NewHubInfoService builds the loki service. oracle supplies the fiat rate for
// GetFiatRate and may be nil (rates then report unavailable until one is
// wired in).
func NewHubInfoService(cfg config.Config, oracle RateOracle) *hubInfoService {
	return &hubInfoService{
		cfg:    cfg,
		oracle: oracle,
	}
}

// GetFiatRate returns the latest rate from the NIP-78 oracle. Only USD is
// supported. An unset/empty/stale oracle surfaces a clear error, which the UI
// degrades to sats/FLC on.
func (svc *hubInfoService) GetFiatRate(ctx context.Context) (*FiatRate, error) {
	if currency := svc.cfg.GetCurrency(); currency != "USD" {
		return nil, errors.New("only USD is supported for now")
	}
	if svc.oracle == nil {
		return nil, errors.New("flokicoin rate unavailable")
	}
	return svc.oracle.Latest()
}

// GetCurrencies returns the embedded currency catalogue, filtered to USD (the
// only supported currency).
func (svc *hubInfoService) GetCurrencies(ctx context.Context) (map[string]FiatCurrency, error) {
	out := make(map[string]FiatCurrency)
	if usd, ok := seed.Get().Currencies["USD"]; ok {
		out["USD"] = FiatCurrency{Name: usd.Name, Symbol: usd.Symbol}
	}
	return out, nil
}

// GetInfo returns the hub info from the embedded seed. The version comes from
// the appversion build tag, falling back to the seed's static value.
func (svc *hubInfoService) GetInfo(ctx context.Context) (*HubInfo, error) {
	info := seed.Get().Info
	version := appversion.Tag
	if version == "" {
		version = info.Version
	}
	return &HubInfo{
		Version:      version,
		ReleaseNotes: info.ReleaseNotes,
	}, nil
}

// GetFAQ returns the embedded FAQ.
func (svc *hubInfoService) GetFAQ(ctx context.Context) ([]FAQ, error) {
	entries := seed.Get().FAQ
	out := make([]FAQ, 0, len(entries))
	for _, e := range entries {
		out = append(out, FAQ{Question: e.Question, Answer: e.Answer})
	}
	return out, nil
}
