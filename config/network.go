package config

import (
	"fmt"
	"os"
	"strings"
)

// networkNames are the NETWORK values lokihub takes, each with the chain it
// names.
var networkNames = map[string]string{
	"":          "mainnet",
	"flokicoin": "mainnet",
	"bitcoin":   "mainnet",
	"mainnet":   "mainnet",
	"testnet":   "testnet",
	"regtest":   "regtest",
	"signet":    "signet",
	"simnet":    "simnet",
}

// nodeChain is the chain a node's reported network names.
func nodeChain(nodeNetwork string) string {
	switch n := strings.ToLower(nodeNetwork); n {
	case "bitcoin", "mainnet", "flokicoin":
		return "mainnet"
	case "testnet", "testnet3", "testnet4":
		return "testnet"
	default:
		return n
	}
}

// CheckNodeNetwork refuses a NETWORK that lokihub does not take, or one that
// names another chain than the node is on: on-chain keys and addresses would
// otherwise use another chain's parameters.
func CheckNodeNetwork(network, nodeNetwork string) error {
	chain, ok := networkNames[network]
	if !ok {
		return fmt.Errorf("NETWORK=%q is not one of flokicoin, mainnet, bitcoin, testnet, regtest, signet or simnet", network)
	}
	if node := nodeChain(nodeNetwork); node != chain {
		return fmt.Errorf("NETWORK=%q names %s, but the node is on %q: set NETWORK to the node's network", network, chain, nodeNetwork)
	}
	return nil
}

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
