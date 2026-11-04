package service

import (
	"context"
	"fmt"

	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/logger"
)

// publishToAll publishes ev to every relay in relayURLs and returns how many
// accepted it. A relay that cannot be reached or refuses is logged and skipped.
// what names the event in the log lines ("the node event").
func publishToAll(ctx context.Context, pool *nostr.SimplePool, relayURLs []string, ev nostr.Event, what string) int {
	published := 0
	for _, relayURL := range relayURLs {
		relay, err := pool.EnsureRelay(relayURL)
		if err != nil {
			logger.Logger.Warn().Err(err).Str("relay", relayURL).
				Msgf("Could not reach relay to publish %s", what)
			continue
		}
		if err := relay.Publish(ctx, ev); err != nil {
			logger.Logger.Warn().Err(err).Str("relay", relayURL).
				Msgf("Failed to publish %s", what)
			continue
		}
		published++
	}
	return published
}

// publishToFirst publishes ev on the preferred relay (the one a request arrived
// on, when known) and, only if that one cannot take it, on the other relays in
// order until one accepts. It fails when none does. what names the event in the
// log lines and the error ("the reply").
func publishToFirst(ctx context.Context, pool *nostr.SimplePool, preferred string, relayURLs []string, ev nostr.Event, what string) error {
	if preferred != "" {
		if relay, err := pool.EnsureRelay(preferred); err != nil {
			logger.Logger.Warn().Err(err).Str("relay", preferred).Msgf("Could not reach the relay a request arrived on to publish %s", what)
		} else if err := relay.Publish(ctx, ev); err != nil {
			logger.Logger.Warn().Err(err).Str("relay", preferred).Msgf("The relay a request arrived on refused %s", what)
		} else {
			return nil
		}
	}
	for _, relayURL := range relayURLs {
		if relayURL == preferred {
			continue
		}
		relay, err := pool.EnsureRelay(relayURL)
		if err != nil {
			continue
		}
		if err := relay.Publish(ctx, ev); err != nil {
			continue
		}
		return nil
	}
	return fmt.Errorf("no relay accepted %s", what)
}
