package history

import (
	"context"
	"encoding/hex"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/logging"
)

type ChannelSync struct {
	nodes []*core.Node
	store Store
}

func NewChannelSync(nodes []*core.Node, store Store) *ChannelSync {
	return &ChannelSync{
		nodes: nodes,
		store: store,
	}
}

var channelSyncInterval time.Duration = time.Minute * 5

func (s *ChannelSync) ChannelsSynchronize(ctx context.Context) {
	s.channelsSynchronizeOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(channelSyncInterval):
		}

		s.channelsSynchronizeOnce(ctx)
	}
}

func (s *ChannelSync) channelsSynchronizeOnce(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(len(s.nodes))
	for _, n := range s.nodes {
		go func(node *core.Node) {
			s.channelsSynchronizeNodeOnce(ctx, node)
			wg.Done()
		}(n)
	}
	wg.Wait()
}

func (s *ChannelSync) channelsSynchronizeNodeOnce(ctx context.Context, node *core.Node) {
	lastUpdate := time.Now()
	nodeIdHex := hex.EncodeToString(node.NodeId)
	log.Debug().
		Str("component", string(logging.LogComponentChannelSync)).
		Str("action", string(logging.LogActionSync)).
		Str("node_id", nodeIdHex).
		Msg("Channel sync started")
	channels, err := node.Client.ListChannels()
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentChannelSync)).
			Str("action", string(logging.LogActionFailure)).
			Str("node_id", nodeIdHex).
			Msg("Failed to list channels from LND")
		return
	}

	updates := make([]*ChannelUpdate, len(channels))
	for i, c := range channels {
		if c == nil {
			continue
		}
		updates[i] = &ChannelUpdate{
			NodeID:        node.NodeId,
			PeerId:        c.PeerId,
			AliasScid:     c.AliasScid,
			ConfirmedScid: c.ConfirmedScid,
			ChannelPoint:  c.ChannelPoint,
			LastUpdate:    lastUpdate,
		}
	}
	err = s.store.UpdateChannels(ctx, updates)
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentChannelSync)).
			Str("action", string(logging.LogActionFailure)).
			Str("node_id", nodeIdHex).
			Msg("Failed to update channels in store")
		return
	}

	log.Debug().
		Str("component", string(logging.LogComponentChannelSync)).
		Str("action", string(logging.LogActionSuccess)).
		Str("node_id", nodeIdHex).
		Int("channel_count", len(channels)).
		Msg("Channel sync completed")
}
