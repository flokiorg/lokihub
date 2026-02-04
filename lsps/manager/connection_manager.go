package manager

import (
	"context"
	"strings"
	"time"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/utils"
)

// ConnectionManager maintains connections to active LSPs
type ConnectionManager struct {
	cfg *ManagerConfig
}

// NewConnectionManager creates a new ConnectionManager
func NewConnectionManager(cfg *ManagerConfig) *ConnectionManager {
	return &ConnectionManager{
		cfg: cfg,
	}
}

// Start begins the connection maintenance loop
func (cm *ConnectionManager) Start(ctx context.Context) {
	logger.Logger.Info().Msg("Starting LSP Connection Manager")

	// Run immediately once
	cm.maintainConnections(ctx)

	// Run periodically
	go func() {
		// Check connections every 2 minutes
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cm.maintainConnections(ctx)
			}
		}
	}()
}

// maintainConnections fetches active LSPs and ensures we are connected to them
func (cm *ConnectionManager) maintainConnections(ctx context.Context) {
	// 1. Get enabled LSPs from store
	lsps, err := cm.cfg.LSPManager.ListLSPs()
	if err != nil {
		logger.Logger.Error().Err(err).Msg("ConnectionManager: Failed to list LSPs")
		return
	}

	// Filter for active LSPs
	var activeLSPs []struct{ Pubkey, Host string }
	for _, lsp := range lsps {
		if lsp.IsActive {
			activeLSPs = append(activeLSPs, struct{ Pubkey, Host string }{lsp.Pubkey, lsp.Host})
		}
	}

	if len(activeLSPs) == 0 {
		return
	}

	// 2. Get current connected peers from the Lightning Node
	peers, err := cm.cfg.LNClient.ListPeers(ctx)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("ConnectionManager: Failed to list peers")
		return
	}

	// Create a set of connected peer pubkeys for fast lookup
	connectedPeers := make(map[string]bool)
	for _, p := range peers {
		if p.NodeId != "" {
			connectedPeers[strings.ToLower(p.NodeId)] = true
		}
	}

	// 3. Connect to missing ones
	for _, lsp := range activeLSPs {
		if !connectedPeers[lsp.Pubkey] {
			logger.Logger.Info().Str("lsp", lsp.Pubkey).Msg("ConnectionManager: LSP disconnected, attempting to connect")

			// Parse host and port
			host, port, err := utils.ParseHostPort(lsp.Host)
			if err != nil {
				logger.Logger.Warn().Err(err).Str("lsp", lsp.Pubkey).Str("host", lsp.Host).Msg("ConnectionManager: Failed to parse LSP host")
				continue
			}

			err = cm.cfg.LNClient.ConnectPeer(ctx, &lnclient.ConnectPeerRequest{
				Pubkey:  lsp.Pubkey,
				Address: host,
				Port:    port,
			})
			if err != nil {
				// Log as warning so we don't spam errors for temporary network issues
				logger.Logger.Warn().Err(err).Str("lsp", lsp.Pubkey).Msg("ConnectionManager: Failed to connect to LSP")
			} else {
				logger.Logger.Info().Str("lsp", lsp.Pubkey).Msg("ConnectionManager: Successfully issued connect request")
			}
		}
	}
}
