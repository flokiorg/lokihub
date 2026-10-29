package manager

import (
	"context"
	"fmt"

	"io"
	"net/http"
	"strings"
	"time"

	"github.com/flokiorg/lokihub/logger"
)

// StartSyncService caches services.json (rates, currencies, info, faq) now and
// every 6 hours.
func (m *LiquidityManager) StartSyncService(ctx context.Context, servicesURL string) {
	if servicesURL == "" {
		logger.Logger.Warn().Msg("No services URL provided for the services sync")
		return
	}

	// Run immediately once
	m.runSync(ctx, servicesURL)

	// Then run periodically (e.g. every 6 hours)
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.runSync(ctx, servicesURL)
			}
		}
	}()
}

func (m *LiquidityManager) runSync(ctx context.Context, servicesURL string) {
	logger.Logger.Info().Msg("Syncing services.json...")
	if err := m.syncRPC(ctx, servicesURL); err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to sync services.json")
	} else {
		logger.Logger.Info().Msg("services.json synced")
	}
}

func (m *LiquidityManager) syncRPC(ctx context.Context, url string) error {
	// Ensure no trailing slash
	url = strings.TrimSuffix(url, "/")

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/services.json", nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	// Read full body first to cache it
	// We read up to 10MB to avoid memory issues, though service.json is tiny
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return err
	}

	// Persist to cache for Frontend API
	if err := m.cfg.AppConfig.SetCachedServicesJSON(string(bodyBytes)); err != nil {
		logger.Logger.Warn().Err(err).Msg("Failed to cache services.json")
	} else {
		logger.Logger.Info().Msg("Cached services.json for local API access")
	}

	// services.json no longer lists LSPs (WP-26, LP-2): the catalogue comes
	// from NIP-LSP announcements. Rates, currencies, info and faq stay.
	return nil
}
