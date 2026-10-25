package config

import (
	"fmt"
	"os"
	"strings"
)

// NetworkRegtest is the NETWORK value of the regtest dev and test stack.
const NetworkRegtest = "regtest"

// supportedNetworks are the NETWORK values lokihub runs on: flokicoin (the
// default), regtest, and the values the swaps code already maps to chain params.
var supportedNetworks = map[string]bool{
	"flokicoin": true,
	"regtest":   true,
	"bitcoin":   true,
	"mainnet":   true,
	"testnet":   true,
	"signet":    true,
}

// ValidateNetwork refuses a NETWORK value lokihub does not support. Empty means
// the default, flokicoin.
func ValidateNetwork(network string) error {
	if network == "" || supportedNetworks[network] {
		return nil
	}
	return fmt.Errorf("unsupported NETWORK %q: use flokicoin or regtest", network)
}

// DevStackEnv is set by the regtest dev stack (docker-compose.dev.yml).
const DevStackEnv = "LOKIHUB_DEV_STACK"

// IsDevStack reports whether this backend runs in the regtest dev stack.
func IsDevStack() bool {
	return os.Getenv(DevStackEnv) == "1"
}

// CheckDevStackNode refuses a dev-stack backend whose Lightning node is not on
// regtest, so the dev stack can never drive a mainnet node.
func CheckDevStackNode(devStack bool, nodeNetwork string) error {
	if !devStack || strings.EqualFold(nodeNetwork, NetworkRegtest) {
		return nil
	}
	return fmt.Errorf("%s=1 but the node reports network %q: the dev stack runs on regtest only", DevStackEnv, nodeNetwork)
}
