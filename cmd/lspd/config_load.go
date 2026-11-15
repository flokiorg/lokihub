package main

import (
	"fmt"
	"os"

	"github.com/flokiorg/lokihub/lspd/adapters/lnd"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"gopkg.in/yaml.v3"
)

// loadNodeConfigs reads and parses the nodes configuration the same way
// runLspd does — NODES_JSON if set, otherwise the file at
// NODES_CONFIG_PATH — so the `flspd config` subcommands (T5-6) inspect
// exactly what the daemon itself would load, not a separate code path
// that could drift from it.
func loadNodeConfigs() ([]*config.NodeConfig, error) {
	var nodesContent []byte
	var err error

	if nodesJson := os.Getenv("NODES_JSON"); nodesJson != "" {
		nodesContent = []byte(nodesJson)
	} else {
		nodesConfigPath := os.Getenv("NODES_CONFIG_PATH")
		if nodesConfigPath == "" {
			return nil, fmt.Errorf("NODES_CONFIG_PATH environment variable is not set")
		}

		nodesContent, err = os.ReadFile(nodesConfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read nodes config file: %w", err)
		}
	}

	var nodeConfigs []*config.NodeConfig
	if err := yaml.Unmarshal(nodesContent, &nodeConfigs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal nodes config: %w", err)
	}

	if len(nodeConfigs) == 0 {
		return nil, fmt.Errorf("need at least one node configured in nodes config file")
	}

	return nodeConfigs, nil
}

// announceRelaysEnv names the relays NIP-LSP announcements are published to
// (and the `r` tags), a comma-separated list (T8-2).
const announceRelaysEnv = "NOSTR_ANNOUNCE_RELAYS"

// checkAnnounceRequirements checks what announcing needs beyond the announce
// block itself (which NodeConfig.Validate checks): relays in
// NOSTR_ANNOUNCE_RELAYS, and for every announcing node a macaroon granting
// signer:generate, so the node key can sign (T8-1). It returns the parsed
// relays. A deployment with no announcing node only has the env var's
// format checked, so nothing changes for it.
func checkAnnounceRequirements(configs []*config.NodeConfig, relaysEnv string) ([]string, error) {
	relays, err := config.ParseAnnounceRelays(relaysEnv)
	if err != nil {
		return nil, err
	}
	for i, cfg := range configs {
		if err := checkNodeAnnounceRequirements(cfg, relays); err != nil {
			return nil, fmt.Errorf("node %d (%s): %w", i, nodeLabel(cfg), err)
		}
	}
	return relays, nil
}

// checkNodeAnnounceRequirements is checkAnnounceRequirements for one node,
// also run on a dashboard hot-swap that turns announcing on.
func checkNodeAnnounceRequirements(cfg *config.NodeConfig, relays []string) error {
	if !cfg.Announcing() {
		return nil
	}
	if len(relays) == 0 {
		return fmt.Errorf("announcing is on but %s is empty; set it to the relays to publish to, e.g. wss://relay.example.com", announceRelaysEnv)
	}
	if cfg.Lnd == nil {
		return fmt.Errorf("announcing needs an flnd block")
	}
	macaroon, err := cfg.Lnd.Macaroon.Resolve()
	if err != nil {
		return fmt.Errorf("flnd.macaroon: %w", err)
	}
	return lnd.CheckSignerMacaroon(macaroon)
}
