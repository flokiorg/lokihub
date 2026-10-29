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
	// Each purchase runs the live check first (NIP-LSP §Client Behavior).
	if lm := svc.liquidityManager; lm != nil {
		catalogue := svc.lspCatalogue
		lm.SetLiveChecker(func(ctx context.Context, key string) (*manager.LiveCheck, error) {
			res, err := catalogue.LiveCheck(ctx, key)
			if err != nil {
				return nil, err
			}
			return &manager.LiveCheck{Usable: res.Usable, Worse: res.Worse, Error: res.Error}, nil
		})
	}
	svc.shutdownGroup.Go(func() error {
		svc.lspCatalogue.Run(ctx)
		return nil
	})
}
