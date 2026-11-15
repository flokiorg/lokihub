package main

import (
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/flokiorg/lokihub/lspd/services"
)

// newNodeLSPSHandlers builds the LSPS handlers that answer on one node. They
// see only that node, so its protocol list, JIT switch, LSPS1 options, fee
// menu and sales are its own, and its announcement says what it answers.
func newNodeLSPSHandlers(
	node *core.Node,
	cfg lsps.Config,
	client lsps.LndClientWrapper,
	openingService ports.OpeningService,
	liquidityManager lsps.LiquidityManager,
	notifications lsps.NotificationService,
	orderWatcher lsps.OrderWatcher,
) (*lsps.Handlers, error) {
	scope, err := services.NewNodesService([]*core.Node{node})
	if err != nil {
		return nil, err
	}
	return lsps.NewHandlers(cfg, client, scope, openingService, liquidityManager, notifications, orderWatcher), nil
}
