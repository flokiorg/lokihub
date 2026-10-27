package service

import (
	"context"
	"errors"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/discovery"
	"github.com/flokiorg/lokihub/lsps/manager"
	"github.com/flokiorg/lokihub/nodesign"
)

// GetLSPCatalogue is the LSP catalogue built from NIP-LSP announcements
// (WP-26), nil until the node is started.
func (svc *service) GetLSPCatalogue() *discovery.Service {
	return svc.lspCatalogue
}

// startLSPCatalogue creates the catalogue and refreshes it from the relays
// until the app stops. Discovery connects to no LSP; only the live check of a
// trusted one does.
func (svc *service) startLSPCatalogue(ctx context.Context, lspManager *manager.LSPManager) {
	ln := func() lnclient.LNClient { return svc.lnClient }
	svc.lspCatalogue = discovery.New(discovery.Deps{
		DB:       lspManager.DB(),
		Settings: svc.cfg,
		Fetcher:  discovery.NostrFetcher{},
		Graph:    discovery.LNGraph{LN: ln},
		Network: func(ctx context.Context) (string, error) {
			if svc.lnClient == nil {
				return "", errors.New("the node is not started")
			}
			info, err := svc.lnClient.GetInfo(ctx)
			if err != nil {
				return "", err
			}
			return nodesign.Network(info.Network), nil
		},
		Live: discovery.ManagerLSPS{LN: ln, LM: func() *manager.LiquidityManager { return svc.liquidityManager }},
	})
	svc.shutdownGroup.Go(func() error {
		svc.lspCatalogue.Run(ctx)
		return nil
	})
}
