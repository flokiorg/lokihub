package nodesign

import "github.com/ohstr/nmilat/nip47"

// Network is NIP-47's network for the node's (NIP-CASH §Network and Chain): one
// of mainnet, testnet, signet or regtest, or "" for a network with no NIP-47
// name, on which the node identity never signs (IdentityNetworkGuard). flnd
// reports mainnet as "bitcoin" or "mainnet" (an older build "flokicoin").
func Network(nodeNetwork string) string {
	switch nodeNetwork {
	case "bitcoin", "mainnet", "flokicoin", "main":
		return nip47.NetworkMainnet
	case "testnet", "testnet3", "testnet4":
		return nip47.NetworkTestnet
	case "signet":
		return nip47.NetworkSignet
	case "regtest", "simnet":
		return nip47.NetworkRegtest
	}
	return ""
}
