package lsps

import "github.com/flokiorg/lokihub/lspd/platform/config"

// SupportedProtocols is the one source of the LSPS protocol list (plan
// F8-Q1): LSPS0 list_protocols answers with it, and the NIP-LSP
// announcement's `protocol` tags are built from it, so the two never differ.
//
//   - 1 (LSPS1) is always offered.
//   - 2 (LSPS2) only while the node has JIT channels enabled.
//   - 5 (LSPS5) only while at least one webhook transport (https or nostr)
//     is enabled.
//
// The list is ascending. A nil nodeCfg is treated as JIT off.
func SupportedProtocols(nodeCfg *config.NodeConfig, c Config) []int {
	protocols := []int{1}
	if nodeCfg != nil && nodeCfg.JitEnabled {
		protocols = append(protocols, 2)
	}
	if c.EnableLsps5Https || c.EnableLsps5Nostr {
		protocols = append(protocols, 5)
	}
	return protocols
}

// SupportedProtocols returns the protocol list for the node these handlers
// serve, from its live config (a dashboard hot-swap of jitEnabled is seen on
// the next call). Handlers serve one node: their NodesService holds only it.
func (s *Handlers) SupportedProtocols() []int {
	var nodeCfg *config.NodeConfig
	if s.nodesService != nil {
		if nodes := s.nodesService.GetNodes(); len(nodes) > 0 && nodes[0].NodeConfig != nil {
			nodeCfg = nodes[0].NodeConfig.Load()
		}
	}
	return SupportedProtocols(nodeCfg, s.config)
}
