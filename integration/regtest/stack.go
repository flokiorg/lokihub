package regtest

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"gopkg.in/yaml.v3"
)

// Amounts of the stack, in loki.
const (
	// ChannelLoki is each channel's capacity; half is pushed to the far side, so
	// each node has outbound and inbound room in both channels.
	ChannelLoki = 5_000_000
	// PushLoki is the part of a channel's capacity pushed to the far side.
	PushLoki = ChannelLoki / 2
)

// Stack is the whole regtest stack.
type Stack struct {
	Chain   *Chain
	Hub     *Node
	Payer   *Node
	Lokihub *Lokihub
	Logf    func(format string, args ...any)
}

// Close disconnects from every node.
func (s *Stack) Close() {
	if s.Chain != nil {
		s.Chain.Close()
	}
	if s.Hub != nil {
		s.Hub.Close()
	}
	if s.Payer != nil {
		s.Payer.Close()
	}
}

func (s *Stack) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// ErrChainBehindNodes means the chain node is behind what the nodes have seen.
var ErrChainBehindNodes = errors.New("the chain node is behind the Lightning nodes")

// CheckChainNotReset refuses a chain whose tip is below a height a node has already
// seen. lokid deletes its regtest block database every time it starts
// (go-flokicoin lokid.go removeRegressionDB), so a restarted chain node comes back
// at height 0 while both wallets remember the old chain; only `just dev wipe`
// recovers from that. The check stays useful once go-flokicoin keeps the database.
func CheckChainNotReset(tip int32, nodeHeights ...int32) error {
	for _, h := range nodeHeights {
		if h > tip {
			return fmt.Errorf("%w (chain tip %d, a node has seen %d): lokid deletes its regtest chain when it restarts; run `just dev wipe` and `just dev up`", ErrChainBehindNodes, tip, h)
		}
	}
	return nil
}

// MineStep mines N blocks to one node's wallet.
type MineStep struct {
	To string // "hub" or "payer"
	N  int
}

// FundingPlan is what a chain at height tip still needs: 101 blocks to the payer,
// 101 to the Hub node (coinbase maturity is 100), then 10 more to the payer so
// early coins are past any final depth. A chain already past a step skips it, so
// a rerun mines nothing more.
func FundingPlan(tip int32) []MineStep {
	targets := []struct {
		to     string
		height int32
	}{{"payer", 101}, {"hub", 202}, {"payer", 212}}
	var plan []MineStep
	for _, t := range targets {
		if tip < t.height {
			plan = append(plan, MineStep{To: t.to, N: int(t.height - tip)})
			tip = t.height
		}
	}
	return plan
}

func (s *Stack) node(name string) (*Node, error) {
	switch name {
	case "hub":
		return s.Hub, nil
	case "payer":
		return s.Payer, nil
	}
	return nil, fmt.Errorf("unknown node %q", name)
}

// MineTo mines n blocks paying the named node's wallet ("hub", "payer") or an
// address, then waits for both nodes to see the new tip.
func (s *Stack) MineTo(ctx context.Context, n int, to string) error {
	address := to
	if to == "hub" || to == "payer" {
		node, _ := s.node(to)
		var err error
		if address, err = node.NewAddress(ctx); err != nil {
			return err
		}
	}
	if _, err := s.Chain.Mine(n, address, nil); err != nil {
		return err
	}
	return s.WaitSynced(ctx)
}

// WaitSynced waits until both nodes are synced to the chain's tip.
func (s *Stack) WaitSynced(ctx context.Context) error {
	tip, err := s.Chain.Tip()
	if err != nil {
		return err
	}
	return poll(ctx, 2*time.Minute, func() (bool, error) {
		for _, n := range []*Node{s.Hub, s.Payer} {
			ok, err := n.Synced(ctx, tip)
			if err != nil || !ok {
				return false, nil //nolint:nilerr // retried until the deadline
			}
		}
		return true, nil
	})
}

// Fund pays amount loki from the payer's wallet to the named node or an address,
// then mines one block.
func (s *Stack) Fund(ctx context.Context, to string, amount int64) error {
	address := to
	if to == "hub" || to == "payer" {
		node, _ := s.node(to)
		var err error
		if address, err = node.NewAddress(ctx); err != nil {
			return err
		}
	}
	if _, err := s.Payer.RPC.SendCoins(ctx, &lnrpc.SendCoinsRequest{Addr: address, Amount: amount, TargetConf: 1}); err != nil {
		return fmt.Errorf("payer send %d to %s: %w", amount, address, err)
	}
	return s.MineTo(ctx, 1, "payer")
}

// hasPeer reports whether n lists pubkey among its connected peers.
func hasPeer(ctx context.Context, n *Node, pubkey string) (bool, error) {
	peers, err := n.RPC.ListPeers(ctx, &lnrpc.ListPeersRequest{})
	if err != nil {
		return false, err
	}
	for _, p := range peers.Peers {
		if p.PubKey == pubkey {
			return true, nil
		}
	}
	return false, nil
}

// ConnectPeers connects the payer to the Hub node and waits until each lists the other.
func (s *Stack) ConnectPeers(ctx context.Context) error {
	connected, err := hasPeer(ctx, s.Payer, s.Hub.Pubkey)
	if err != nil {
		return err
	}
	if !connected {
		s.logf("connecting payer to hub at %s", s.Hub.Config.P2P)
		_, err = s.Payer.RPC.ConnectPeer(ctx, &lnrpc.ConnectPeerRequest{
			Addr: &lnrpc.LightningAddress{Pubkey: s.Hub.Pubkey, Host: s.Hub.Config.P2P},
			Perm: true,
		})
		if err != nil && !strings.Contains(err.Error(), "already connected") {
			return fmt.Errorf("connect payer to hub: %w", err)
		}
	}
	return poll(ctx, time.Minute, func() (bool, error) {
		a, errA := hasPeer(ctx, s.Payer, s.Hub.Pubkey)
		b, errB := hasPeer(ctx, s.Hub, s.Payer.Pubkey)
		return errA == nil && errB == nil && a && b, nil
	})
}

// openedBy reports whether opener has an open or pending channel it opened to peer.
func openedBy(ctx context.Context, opener *Node, peer string) (bool, error) {
	chans, err := opener.RPC.ListChannels(ctx, &lnrpc.ListChannelsRequest{Peer: mustHex(peer)})
	if err != nil {
		return false, err
	}
	for _, c := range chans.Channels {
		if c.Initiator {
			return true, nil
		}
	}
	pending, err := opener.RPC.PendingChannels(ctx, &lnrpc.PendingChannelsRequest{})
	if err != nil {
		return false, err
	}
	for _, c := range pending.PendingOpenChannels {
		if c.Channel.RemoteNodePub == peer && c.Channel.Initiator == lnrpc.Initiator_INITIATOR_LOCAL {
			return true, nil
		}
	}
	return false, nil
}

func mustHex(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}

// EnsureChannels opens a channel from each side that has none (Hub → payer and
// payer → Hub), mines 6 blocks and waits until every channel is active.
func (s *Stack) EnsureChannels(ctx context.Context) error {
	if err := s.ConnectPeers(ctx); err != nil {
		return err
	}
	opened := false
	for _, pair := range [][2]*Node{{s.Hub, s.Payer}, {s.Payer, s.Hub}} {
		opener, peer := pair[0], pair[1]
		has, err := openedBy(ctx, opener, peer.Pubkey)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		s.logf("opening a %d loki channel %s -> %s", ChannelLoki, opener.Config.Name, peer.Config.Name)
		if _, err := opener.RPC.OpenChannelSync(ctx, &lnrpc.OpenChannelRequest{
			NodePubkey:         mustHex(peer.Pubkey),
			LocalFundingAmount: ChannelLoki,
			PushSat:            PushLoki,
		}); err != nil {
			return fmt.Errorf("open channel %s -> %s: %w", opener.Config.Name, peer.Config.Name, err)
		}
		opened = true
	}
	if opened {
		if err := s.MineTo(ctx, 6, "payer"); err != nil {
			return err
		}
	}
	return poll(ctx, 2*time.Minute, func() (bool, error) {
		for _, n := range []*Node{s.Hub, s.Payer} {
			chans, err := n.RPC.ListChannels(ctx, &lnrpc.ListChannelsRequest{ActiveOnly: true})
			if err != nil {
				return false, nil //nolint:nilerr // retried until the deadline
			}
			if len(chans.Channels) < 2 {
				return false, nil
			}
		}
		return true, nil
	})
}

// SetupLokihub completes lokihub's first-run setup, starts it, mints an admin JWT
// and points its GeneralRelay at the dev relay. Each step is skipped when done.
func (s *Stack) SetupLokihub(ctx context.Context, password, relay string) (string, error) {
	var info Info
	if err := poll(ctx, 3*time.Minute, func() (bool, error) {
		var err error
		info, err = s.Lokihub.Info(ctx)
		return err == nil, nil
	}); err != nil {
		return "", fmt.Errorf("lokihub API at %s: %w", s.Lokihub.BaseURL, err)
	}
	if !info.SetupCompleted {
		s.logf("running lokihub first-run setup")
		if err := s.Lokihub.Setup(ctx, password); err != nil {
			return "", err
		}
	}
	if !info.Running {
		s.logf("starting lokihub")
		if err := s.Lokihub.Start(ctx, password); err != nil && !strings.Contains(err.Error(), "already started") {
			return "", err
		}
	}
	if err := poll(ctx, 3*time.Minute, func() (bool, error) {
		info, err := s.Lokihub.Info(ctx)
		return err == nil && info.Running, nil
	}); err != nil {
		return "", fmt.Errorf("lokihub did not start: %w", err)
	}
	token, err := s.Lokihub.Unlock(ctx, password, 365)
	if err != nil {
		return "", err
	}
	if err := s.Lokihub.SetGeneralRelay(ctx, token, relay); err != nil {
		return "", err
	}
	return token, nil
}

// IntegrationConfig is integration/config.local.yaml as bootstrap writes it.
type IntegrationConfig struct {
	AdminAPI struct {
		BaseURL string `yaml:"base_url"`
		Token   string `yaml:"token"`
	} `yaml:"admin_api"`
	RelayURL string        `yaml:"relay_url"`
	Regtest  RegtestConfig `yaml:"regtest"`
}

// RegtestConfig is the config's regtest block: how a test reaches the chain and the payer.
type RegtestConfig struct {
	ChainRPC      string `yaml:"chain_rpc"`
	ChainRPCUser  string `yaml:"chain_rpc_user"`
	ChainRPCPass  string `yaml:"chain_rpc_pass"`
	ChainRPCCert  string `yaml:"chain_rpc_cert"`
	PayerGRPC     string `yaml:"payer_grpc"`
	PayerMacaroon string `yaml:"payer_macaroon"`
	PayerTLSCert  string `yaml:"payer_tls_cert"`
}

// WriteConfig writes c to path, readable by its owner only (it holds a token).
func WriteConfig(path string, c IntegrationConfig) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	header := "# Written by `just dev bootstrap` (integration/cmd/regtest). Points at the regtest\n# dev stack only; never point this suite at a mainnet Hub.\n"
	return os.WriteFile(path, append([]byte(header), data...), 0o600)
}

var errTimeout = errors.New("timed out")

// poll calls f every second until it reports done, ctx ends or d passes.
func poll(ctx context.Context, d time.Duration, f func() (bool, error)) error {
	deadline := time.Now().Add(d)
	for {
		done, err := f()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return errTimeout
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
