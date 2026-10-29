package discovery

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip01"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/manager"
	"github.com/flokiorg/lokihub/utils"
)

// NostrFetcher is Fetcher over go-nostr, one short-lived pool per call.
type NostrFetcher struct{}

// Fetch reads the events matching f from relays until each relay's EOSE (10 s
// at most), deduplicated by id; events that do not verify are dropped.
func (NostrFetcher) Fetch(ctx context.Context, relays []string, f nip01.SubscriptionFilter) ([]*nip01.Event, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool := nostr.NewSimplePool(cctx)
	filter := nostr.Filter{Authors: f.Authors, Kinds: f.Kinds, Limit: f.Limit}
	seen := map[string]bool{}
	var out []*nip01.Event
	for ie := range pool.FetchMany(cctx, relays, filter) {
		if ie.Event == nil || seen[ie.ID] {
			continue
		}
		seen[ie.ID] = true
		tags := make([][]string, 0, len(ie.Tags))
		for _, t := range ie.Tags {
			tags = append(tags, []string(t))
		}
		out = append(out, &nip01.Event{ID: ie.ID, PubKey: ie.PubKey, CreatedAt: uint64(ie.CreatedAt), //nolint:gosec // a unix time
			Kind: ie.Kind, Tags: tags, Content: ie.Content, Sig: ie.Sig})
	}
	return out, nil
}

// LNGraph is Graph over the node's own channel graph.
type LNGraph struct {
	LN func() lnclient.LNClient
}

// Facts reads presence, channel count and capacity of nodeIDs from the graph.
func (g LNGraph) Facts(ctx context.Context, nodeIDs []string) (map[string]GraphFact, error) {
	ln := g.LN()
	if ln == nil {
		return nil, nil
	}
	resp, err := ln.GetNetworkGraph(ctx, nodeIDs)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	var graph struct {
		Nodes []struct {
			NodeID string `json:"nodeId"`
		} `json:"nodes"`
		Channels []struct {
			Node1Pub string          `json:"node1_pub"`
			Node2Pub string          `json:"node2_pub"`
			Capacity json.RawMessage `json:"capacity"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(raw, &graph); err != nil {
		return nil, err
	}
	out := map[string]GraphFact{}
	for _, n := range graph.Nodes {
		out[strings.ToLower(n.NodeID)] = GraphFact{Present: true}
	}
	for _, c := range graph.Channels {
		var capacity int64
		_ = json.Unmarshal([]byte(strings.Trim(string(c.Capacity), `"`)), &capacity)
		for _, id := range []string{strings.ToLower(c.Node1Pub), strings.ToLower(c.Node2Pub)} {
			if f, ok := out[id]; ok {
				f.Channels++
				f.Capacity += capacity
				out[id] = f
			}
		}
	}
	return out, nil
}

// ManagerLSPS is LSPSClient over the liquidity manager's LSPS clients.
type ManagerLSPS struct {
	LN func() lnclient.LNClient
	LM func() *manager.LiquidityManager
}

// Connect connects the node to nodeID at addr; an existing connection is fine.
func (m ManagerLSPS) Connect(ctx context.Context, nodeID, addr string) error {
	host, port, err := utils.ParseHostPort(addr)
	if err != nil {
		return err
	}
	err = m.LN().ConnectPeer(ctx, &lnclient.ConnectPeerRequest{Pubkey: nodeID, Address: host, Port: port})
	if err != nil && strings.Contains(err.Error(), "already connected") {
		return nil
	}
	return err
}

// ListProtocols is LSPS0 list_protocols.
func (m ManagerLSPS) ListProtocols(ctx context.Context, nodeID string) ([]int, error) {
	return m.LM().LSPS0Client().ListProtocols(ctx, nodeID)
}

// LSPS1Info is LSPS1 get_info in lokihub's _loki dialect.
func (m ManagerLSPS) LSPS1Info(ctx context.Context, nodeID string) (json.RawMessage, error) {
	opts, err := m.LM().GetLSPS1Info(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(opts)
}

// LSPS2Menu is LSPS2 get_info's menu in lokihub's _mloki dialect.
func (m ManagerLSPS) LSPS2Menu(ctx context.Context, nodeID string) (json.RawMessage, error) {
	menu, err := m.LM().GetLSPS2FeeParams(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"opening_fee_params_menu": menu})
}
