package nodesign

import "github.com/ohstr/nmilat/nip47"

// Network is NIP-47's network for the node's: flnd reports mainnet as "bitcoin"
// or "mainnet" (and an older build "flokicoin"); the test networks keep their
// names. get_info and every listing report it (NIP-CASH §Network and Chain).
func Network(nodeNetwork string) string {
	switch nodeNetwork {
	case "bitcoin", "mainnet", "flokicoin", "main":
		return nip47.NetworkMainnet
	case "testnet", "testnet3":
		return nip47.NetworkTestnet
	}
	return nodeNetwork
}
