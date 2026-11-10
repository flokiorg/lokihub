package regtest

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flokiorg/flnd/lnrpc"

	"github.com/flokiorg/lokihub/lnclient/flnd/wrapper"
)

// NodeConfig names one flnd's gRPC endpoint and data directory (its .flnd).
type NodeConfig struct {
	Name    string // "hub" or "payer"
	Address string // host:port of the gRPC listener
	Dir     string // the node's .flnd directory, for admin.macaroon and tls.cert
	P2P     string // host:port the other node dials, inside the compose network
}

// MacaroonPath is the regtest admin macaroon under a .flnd directory.
func MacaroonPath(flndDir string) string {
	return filepath.Join(flndDir, "data", "chain", "flokicoin", Params.Name, "admin.macaroon")
}

// Node is one flnd on the stack.
type Node struct {
	Config NodeConfig
	RPC    *wrapper.FLNDWrapper
	Pubkey string
	Height int32 // block height the node had seen when dialled
}

// DialNode connects to a node and refuses one whose network is not regtest.
func DialNode(ctx context.Context, cfg NodeConfig) (*Node, error) {
	mac, err := os.ReadFile(MacaroonPath(cfg.Dir))
	if err != nil {
		return nil, fmt.Errorf("%s: read admin macaroon: %w", cfg.Name, err)
	}
	cert, err := os.ReadFile(filepath.Join(cfg.Dir, "tls.cert"))
	if err != nil {
		return nil, fmt.Errorf("%s: read tls.cert: %w", cfg.Name, err)
	}
	rpc, err := wrapper.NewFLNDclient(wrapper.FLNDoptions{
		Address:     cfg.Address,
		CertHex:     hex.EncodeToString(cert),
		MacaroonHex: hex.EncodeToString(mac),
	})
	if err != nil {
		return nil, fmt.Errorf("%s: connect: %w", cfg.Name, err)
	}
	info, err := rpc.GetInfo(ctx, &lnrpc.GetInfoRequest{})
	if err != nil {
		_ = rpc.Close()
		return nil, fmt.Errorf("%s: getinfo: %w", cfg.Name, err)
	}
	if err := CheckNodeNetwork(cfg.Name, info); err != nil {
		_ = rpc.Close()
		return nil, err
	}
	return &Node{Config: cfg, RPC: rpc, Pubkey: info.IdentityPubkey, Height: int32(info.BlockHeight)}, nil //nolint:gosec // regtest heights are small
}

// CheckNodeNetwork refuses a node whose GetInfo network is not regtest.
func CheckNodeNetwork(name string, info *lnrpc.GetInfoResponse) error {
	if info == nil || len(info.Chains) == 0 || info.Chains[0].Network != Params.Name {
		network := ""
		if info != nil && len(info.Chains) > 0 {
			network = info.Chains[0].Network
		}
		return fmt.Errorf("%w: %s reports network %q", ErrNotRegtest, name, network)
	}
	return nil
}

// Close disconnects.
func (n *Node) Close() { _ = n.RPC.Close() }

// NewAddress is a fresh P2WKH address of the node's wallet.
func (n *Node) NewAddress(ctx context.Context) (string, error) {
	resp, err := n.RPC.NewAddress(ctx, &lnrpc.NewAddressRequest{Type: lnrpc.AddressType_WITNESS_PUBKEY_HASH})
	if err != nil {
		return "", fmt.Errorf("%s: new address: %w", n.Config.Name, err)
	}
	return resp.Address, nil
}

// Synced reports whether the node is synced to the chain at height tip.
func (n *Node) Synced(ctx context.Context, tip int32) (bool, error) {
	info, err := n.RPC.GetInfo(ctx, &lnrpc.GetInfoRequest{})
	if err != nil {
		return false, err
	}
	return info.SyncedToChain && int32(info.BlockHeight) >= tip, nil //nolint:gosec // regtest heights are small
}
