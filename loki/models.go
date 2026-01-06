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
