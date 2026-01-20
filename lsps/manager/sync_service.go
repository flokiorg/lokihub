package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/utils"
)

// StartSyncService starts the background sync service for community LSPs
func (m *LiquidityManager) StartSyncService(ctx context.Context, servicesURL string) {
	if servicesURL == "" {
		logger.Logger.Warn().Msg("No services URL provided for LSP sync service")
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
	logger.Logger.Info().Msg("Starting community LSP sync...")
	if err := m.syncRPC(ctx, servicesURL); err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to sync community LSPs")
	} else {
		logger.Logger.Info().Msg("Community LSP sync completed")
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	type ExternalLSP struct {
		Name        string `json:"name"`
		URI         string `json:"uri"`
		Pubkey      string `json:"pubkey"`
		Host        string `json:"host"`
		Description string `json:"description"`
	}
	type ServiceConfig struct {
		LSPs []ExternalLSP `json:"lsps"`
	}

	var result ServiceConfig
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if len(result.LSPs) == 0 {
		return nil
	}

	var inputs []CommunityLSPInput
	for _, l := range result.LSPs {
		pubkey := l.Pubkey
		host := l.Host

		if l.URI != "" {
			pk, h, err := utils.ParseLSPURI(l.URI)
			if err == nil {
				pubkey = pk
				host = h
			}
		}

		inputs = append(inputs, CommunityLSPInput{
			Name:        l.Name,
			Description: l.Description,
			Pubkey:      pubkey,
			Host:        host,
		})
	}

	return m.cfg.LSPManager.SyncSystemLSPs(inputs)
}
