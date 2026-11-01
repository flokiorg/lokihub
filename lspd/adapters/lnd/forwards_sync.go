package lnd

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/lokihub/lspd/history"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/platform/logging"
)

type ForwardSync struct {
	nodeid []byte
	client *LndClient
	store  history.Store
}

func NewForwardSync(
	nodeid []byte,
	client *LndClient,
	store history.Store,
) *ForwardSync {
	return &ForwardSync{
		nodeid: nodeid,
		client: client,
		store:  store,
	}
}

var forwardChannelSyncInterval time.Duration = time.Minute * 5

const maxEvents = 10_000

func (s *ForwardSync) ForwardsSynchronize(ctx context.Context) {
	s.forwardsSynchronizeOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(forwardChannelSyncInterval):
		}

		s.forwardsSynchronizeOnce(ctx)
	}
}

func (s *ForwardSync) forwardsSynchronizeOnce(ctx context.Context) {
	last, err := s.store.FetchLndForwardOffset(ctx, s.nodeid)
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentForwardsSync)).
			Str("action", string(logging.LogActionFailure)).
			Str("node_id", fmt.Sprintf("%x", s.nodeid)).
			Msg("Forwards Sync: Failed to fetch offset")
		return
	}

	var startTime uint64
	if last != nil {
		startTime = uint64(last.UnixNano())
	}

	for {
		forwardHistory, err := s.client.client.ForwardingHistory(context.Background(), &lnrpc.ForwardingHistoryRequest{
			StartTime:    startTime / 1_000_000_000,
			NumMaxEvents: maxEvents,
		})
		if err != nil {
			log.Error().
				Err(err).
				Str("component", string(logging.LogComponentForwardsSync)).
				Str("action", string(logging.LogActionFailure)).
				Str("node_id", fmt.Sprintf("%x", s.nodeid)).
				Msg("Forwards Sync: Failed to fetch forwarding history from LND")
			return
		}

		log.Debug().
			Str("component", string(logging.LogComponentForwardsSync)).
			Str("action", string(logging.LogActionSync)).
			Str("node_id", fmt.Sprintf("%x", s.nodeid)).
			Uint64("start_time", startTime).
			Int("events_count", len(forwardHistory.ForwardingEvents)).
			Msg("Forwards Sync: Processing batch")
		if len(forwardHistory.ForwardingEvents) == 0 {
			break
		}

		forwards := make([]*history.Forward, len(forwardHistory.ForwardingEvents))
		for i, f := range forwardHistory.ForwardingEvents {
			forwards[i] = &history.Forward{
				Identifier:   strconv.FormatUint(f.TimestampNs, 10) + "|" + strconv.FormatUint(f.AmtInMsat, 10),
				InChannel:    lightning.ShortChannelID(f.ChanIdIn),
				OutChannel:   lightning.ShortChannelID(f.ChanIdOut),
				InMloki:      f.AmtInMsat,
				OutMloki:     f.AmtOutMsat,
				ResolvedTime: time.Unix(0, int64(f.TimestampNs)),
			}
			startTime = f.TimestampNs
		}
		err = s.store.InsertForwards(ctx, forwards, s.nodeid)
		if err != nil {
			log.Error().
				Err(err).
				Str("component", string(logging.LogComponentForwardsSync)).
				Str("action", string(logging.LogActionFailure)).
				Str("node_id", fmt.Sprintf("%x", s.nodeid)).
				Msg("Forwards Sync: Failed to insert forwards into store")
			return
		}

		if len(forwardHistory.ForwardingEvents) < maxEvents {
			break
		}
	}
}
