package tests

import (
	"context"

	"github.com/flokiorg/lokihub/logger"
	"github.com/nbd-wtf/go-nostr"
)

type mockSimplePool struct {
	PublishedEvents []*nostr.Event
}

func NewMockSimplePool() *mockSimplePool {
	return &mockSimplePool{}
}

func (relay *mockSimplePool) PublishMany(ctx context.Context, relayUrls []string, event nostr.Event) chan nostr.PublishResult {
	logger.Logger.Info().Interface("event", event).Msg("Mock Publishing event")
	relay.PublishedEvents = append(relay.PublishedEvents, &event)

	channel := make(chan nostr.PublishResult)
	go func() {
		channel <- nostr.PublishResult{
			RelayURL: "wss://fakerelay.com/v1",
		}
		close(channel)
	}()
	return channel
}

func (relay *mockSimplePool) QuerySingle(
	ctx context.Context,
	urls []string,
	filter nostr.Filter,
	opts ...nostr.SubscriptionOption,
) *nostr.RelayEvent {
	logger.Logger.Error().Msg("Mock pool QuerySingle is not supported yet")
	return nil
}
