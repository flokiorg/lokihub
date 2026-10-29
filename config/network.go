package config

import (
	"fmt"
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
// names another chain than the node is on: swap keys, swap addresses and the
// swap service would otherwise use another chain's parameters.
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
