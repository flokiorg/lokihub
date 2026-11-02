package loki

import (
	"context"
)

type LokiService interface {
	GetInfo(ctx context.Context) (*LokiInfo, error)
	GetFlokicoinRate(ctx context.Context) (*FlokicoinRate, error)
	GetCurrencies(ctx context.Context) (map[string]LokiCurrency, error)
	GetFAQ(ctx context.Context) ([]FAQ, error)
}

// RateOracle supplies the latest Flokicoin fiat rate fetched from the NIP-78
// oracle (see package ratesoracle). GetFlokicoinRate reads through it. Latest
// returns an error when no rate has been fetched yet or the last-good one is
// stale, which the UI degrades to sats/FLC on.
type RateOracle interface {
	Latest() (*FlokicoinRate, error)
}

type FAQ struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type LokiCurrency struct {
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
}

type LokiInfo struct {
	Version      string `json:"version"`
	ReleaseNotes string `json:"releaseNotes"` // Markdown format
}

type FlokicoinRate struct {
	Code      string  `json:"code"`
	Symbol    string  `json:"symbol"`
	Rate      string  `json:"rate"`
	RateFloat float64 `json:"rate_float"`
}

type ErrorResponse struct {
	Message string `json:"message"`
}
