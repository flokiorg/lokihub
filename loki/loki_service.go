package loki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/logger"
)

type lokiService struct {
	cfg config.Config
}

func NewLokiService(cfg config.Config) *lokiService {
	lokiSvc := &lokiService{
		cfg: cfg,
	}
	return lokiSvc
}

func (svc *lokiService) GetFlokicoinRate(ctx context.Context) (*FlokicoinRate, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	currency := svc.cfg.GetCurrency()

	if currency != "USD" {
		return nil, errors.New("only USD is supported for now")
	}

	url := fmt.Sprintf("%s/rates.json", svc.cfg.GetLokihubServicesURL())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		logger.Logger.Error().
			Str("currency", currency).
			Err(err).
			Msg("Error creating request to Flokicoin rate endpoint")
		return nil, err
	}
	setDefaultRequestHeaders(req)

	res, err := client.Do(req)
	if err != nil {
		logger.Logger.Error().
			Str("currency", currency).
			Err(err).
			Msg("Failed to fetch Flokicoin rate from API")
		return nil, err
	}

	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		logger.Logger.Error().Err(err).
			Str("url", url).
			Msg("Failed to read response body")
		return nil, errors.New("failed to read response body")
	}

	if res.StatusCode >= 300 {
		logger.Logger.Error().
			Str("currency", currency).
			Str("body", string(body)).
			Int("status_code", res.StatusCode).
			Msg("Flokicoin rate endpoint returned non-success code")
		return nil, fmt.Errorf("flokicoin rate endpoint returned non-success code: %s", string(body))
	}

	var rate = &FlokicoinRate{}
	err = json.Unmarshal(body, rate)
	if err != nil {
		logger.Logger.Error().
			Str("currency", currency).
			Str("body", string(body)).
			Err(err).
			Msg("Failed to decode Flokicoin rate API response")
		return nil, err
	}

	return rate, nil
}

func (svc *lokiService) GetCurrencies(ctx context.Context) (map[string]LokiCurrency, error) {
	// Create a new context with timeout to avoid issues with parent context (e.g. Wails context)
	// being cancelled or behaving unexpectedly during the request.
	reqCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 10 * time.Second}

	url := fmt.Sprintf("%s/currencies.json", svc.cfg.GetLokihubServicesURL())

	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Error creating request to rates endpoint")
		return nil, err
	}
	setDefaultRequestHeaders(req)

	res, err := client.Do(req)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch rates from API")
		// Wrap error to help debugging on frontend
		return nil, fmt.Errorf("backend failed to fetch rates: %w", err)
	}

	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		logger.Logger.Error().Err(err).
			Str("url", url).
			Msg("Failed to read response body")
		return nil, errors.New("failed to read response body")
	}

	if res.StatusCode >= 300 {
		logger.Logger.Error().
			Str("body", string(body)).
			Int("status_code", res.StatusCode).
			Msg("Rates endpoint returned non-success code")
		return nil, fmt.Errorf("rates endpoint returned non-success code: %s", string(body))
	}

	var currencies map[string]LokiCurrency
	err = json.Unmarshal(body, &currencies)
	if err != nil {
		logger.Logger.Error().
			Str("body", string(body)).
			Err(err).
			Msg("Failed to decode rates API response")
		return nil, err
	}

	// Filter to only allow USD
	filteredCurrencies := make(map[string]LokiCurrency)
	if usd, ok := currencies["USD"]; ok {
		filteredCurrencies["USD"] = usd
	}

	return filteredCurrencies, nil
}

func (svc *lokiService) GetInfo(ctx context.Context) (*LokiInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("%s/info.json", svc.cfg.GetLokihubServicesURL())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	setDefaultRequestHeaders(req)

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("info endpoint returned non-success code: %s", string(body))
	}

	var info LokiInfo
	err = json.Unmarshal(body, &info)
	if err != nil {
		return nil, err
	}

	return &info, nil
}

func (svc *lokiService) GetFAQ(ctx context.Context) ([]FAQ, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("%s/faq.json", svc.cfg.GetLokihubServicesURL())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	setDefaultRequestHeaders(req)

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("faq endpoint returned non-success code: %s", string(body))
	}

	var faqs []FAQ
	err = json.Unmarshal(body, &faqs)
	if err != nil {
		return nil, err
	}

	return faqs, nil
}

func setDefaultRequestHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Lokihub")
	req.Header.Set("Content-Type", "application/json")
}
