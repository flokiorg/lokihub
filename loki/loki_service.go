package loki

import (
	"context"
	"errors"

	"github.com/flokiorg/lokihub/appversion"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/seed"
)

type lokiService struct {
	cfg    config.Config
	oracle RateOracle
}

// NewLokiService builds the loki service. oracle supplies the fiat rate for
// GetFlokicoinRate and may be nil (rates then report unavailable until one is
// wired in).
func NewLokiService(cfg config.Config, oracle RateOracle) *lokiService {
	return &lokiService{
		cfg:    cfg,
		oracle: oracle,
	}
}

// GetFlokicoinRate returns the latest rate from the NIP-78 oracle. Only USD is
// supported. An unset/empty/stale oracle surfaces a clear error, which the UI
// degrades to sats/FLC on.
func (svc *lokiService) GetFlokicoinRate(ctx context.Context) (*FlokicoinRate, error) {
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
func (svc *lokiService) GetCurrencies(ctx context.Context) (map[string]LokiCurrency, error) {
	out := make(map[string]LokiCurrency)
	if usd, ok := seed.Get().Currencies["USD"]; ok {
		out["USD"] = LokiCurrency{Name: usd.Name, Symbol: usd.Symbol}
	}
	return out, nil
}

// GetInfo returns the hub info from the embedded seed. The version comes from
// the appversion build tag, falling back to the seed's static value.
func (svc *lokiService) GetInfo(ctx context.Context) (*LokiInfo, error) {
	info := seed.Get().Info
	version := appversion.Tag
	if version == "" {
		version = info.Version
	}
	return &LokiInfo{
		Version:      version,
		ReleaseNotes: info.ReleaseNotes,
	}, nil
}

// GetFAQ returns the embedded FAQ.
func (svc *lokiService) GetFAQ(ctx context.Context) ([]FAQ, error) {
	entries := seed.Get().FAQ
	out := make([]FAQ, 0, len(entries))
	for _, e := range entries {
		out = append(out, FAQ{Question: e.Question, Answer: e.Answer})
	}
	return out, nil
}
