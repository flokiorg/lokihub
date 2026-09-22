package service

import (
	"context"
	"errors"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"golang.org/x/sync/errgroup"

	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
)

// droppedEventLogInterval is how often the count of events discarded by the
// gate is summarised. Summarised, never logged per event: formatting and IO
// on a path an attacker can drive is itself a denial of service.
const droppedEventLogInterval = time.Minute

// walletPubkeyHexLen is the length of a hex-encoded Nostr pubkey. Checking it
// rejects malformed "p" tags before hashing anything.
const walletPubkeyHexLen = 64

// startTrustedRelaySubscription opens ONE subscription covering every NIP-47
// request on the hub's own relays, in place of one subscription per app
// wallet.
//
// A cash token is an NWC connection, so per-wallet subscriptions grow with
// bills in circulation rather than with users — a hub handing out cash would
// hold thousands. A kind-only filter costs one subscription no matter how
// many wallets exist.
//
// It also removes a race. NIP-47 requests are ephemeral (kind 23194): a relay
// delivers them only to whoever is attached at that instant and never replays
// them. A per-wallet subscription is opened asynchronously after its app
// exists, while the caller already holds a pairing URI, so a request
// published into that window reached nobody and the caller waited out its
// deadline. This filter is opened once and never changes, so a new wallet is
// covered before it exists.
//
// The cost is that matching moves from the relay to here: every kind-23194
// event on the relay now arrives, including anything an attacker publishes.
// acceptsRequestEvent is the gate, and it runs before any goroutine is
// spawned.
func (svc *service) startTrustedRelaySubscription(ctx context.Context, pool *nostr.SimplePool, group *errgroup.Group) error {
	svc.logDroppedEventsPeriodically(ctx, group)

	filter := nostr.Filter{
		Kinds: []int{models.REQUEST_KIND},
	}

	for {
		subCtx, cancelSubscription := context.WithCancel(ctx)
		eventsChannel := pool.SubscribeMany(subCtx, svc.cfg.GetRelayUrls(), filter)

		err := svc.watchTrustedSubscription(subCtx, pool, eventsChannel, group)
		cancelSubscription()

		if err != nil && ctx.Err() == nil {
			logger.Logger.Error().Err(err).Msg("got an error from the relay while listening to the NIP-47 request subscription, resubscribing")
			time.Sleep(3 * time.Second)
			continue
		}
		return nil
	}
}

// watchTrustedSubscription is watchSubscription's counterpart for the shared
// subscription: same lifecycle, but each event passes the gate before a
// goroutine is spawned for it.
func (svc *service) watchTrustedSubscription(ctx context.Context, pool *nostr.SimplePool, eventsChannel chan nostr.RelayEvent, group *errgroup.Group) error {
	// Buffered for the same reason as watchSubscription's: the inner
	// goroutine must be able to send after the outer select has already
	// returned via ctx.Done().
	eventsChannelClosed := make(chan struct{}, 1)

	go func() {
		for event := range eventsChannel {
			select {
			case <-ctx.Done():
				return
			default:
				// Synchronously, before group.Go: spawning a goroutine per
				// inbound event would let anyone publishing junk to the relay
				// drive this hub's scheduler.
				if !svc.acceptsRequestEvent(event.Event) {
					continue
				}

				group.Go(func() error {
					defer func() {
						if r := recover(); r != nil {
							logger.Logger.Error().Interface("panic", r).Msg("recovered panic in NIP-47 event handling")
						}
					}()
					svc.nip47Service.HandleEvent(ctx, pool, event.Event, svc.lnClient)
					return nil
				})
			}
		}
		logger.Logger.Debug().Msg("NIP-47 request subscription events channel ended")
		eventsChannelClosed <- struct{}{}
	}()

	select {
	case <-ctx.Done():
		logger.Logger.Trace().Msg("Exiting NIP-47 request subscription due to context exit...")
		return nil
	case <-eventsChannelClosed:
		// Same contract as watchSubscription: go-nostr closes the channel on
		// a relay CLOSE, and returning an error triggers a resubscribe.
		logger.Logger.Info().Msg("NIP-47 request subscription was exited abnormally")
		return errors.New("subscription exited abnormally")
	}
}

// acceptsRequestEvent decides whether an inbound event is one of ours, as
// cheaply as possible: the relay no longer filters by "p" for us, so this
// runs for every kind-23194 event on the relay, junk included.
//
// Ordered cheapest first, and deliberately allocation-free — a rejected event
// costs a couple of comparisons and one map probe, no lock and no garbage.
func (svc *service) acceptsRequestEvent(event *nostr.Event) bool {
	if event == nil || event.Kind != models.REQUEST_KIND {
		svc.droppedRequestEvents.Add(1)
		return false
	}

	walletPubkey := firstPTagValue(event)
	if len(walletPubkey) != walletPubkeyHexLen {
		svc.droppedRequestEvents.Add(1)
		return false
	}

	if !svc.walletRegistry.Has(walletPubkey) {
		svc.droppedRequestEvents.Add(1)
		// Debug, not higher: this runs for every junk event on the relay, and
		// logging at a level that is on by default would hand an attacker an
		// amplification path. The minute summary is the operator-facing signal.
		logger.Logger.Debug().
			Str("wallet", walletPubkey).
			Int("registry_size", svc.walletRegistry.Len()).
			Msg("Discarded a NIP-47 request for a wallet this hub does not serve")
		return false
	}

	return true
}

// firstPTagValue returns the event's first "p" tag value, or "" if it has
// none. Hand-rolled rather than nostr.Tags.GetFirst, which takes a []string
// prefix and so allocates on a path that runs for every junk event.
func firstPTagValue(event *nostr.Event) string {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "p" {
			return tag[1]
		}
	}
	return ""
}

// logDroppedEventsPeriodically reports what the gate discarded, on a timer
// rather than per event. A steady nonzero count means someone is publishing
// NIP-47 requests this hub does not serve — junk, or another hub sharing the
// relay.
func (svc *service) logDroppedEventsPeriodically(ctx context.Context, group *errgroup.Group) {
	group.Go(func() error {
		ticker := time.NewTicker(droppedEventLogInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if dropped := svc.droppedRequestEvents.Swap(0); dropped > 0 {
					logger.Logger.Info().
						Int64("dropped", dropped).
						Dur("interval", droppedEventLogInterval).
						Int("wallets", svc.walletRegistry.Len()).
						Msg("Discarded NIP-47 requests addressed to wallets this hub does not serve")
				}
			}
		}
	})
}

// applyTrustedRelayOptions marks this hub's own NWC relays as trusted, which
// skips re-verifying signatures on the events they deliver: the relay already
// verified them on ingest, so checking again is duplicate secp256k1 work that
// anyone publishing to the relay can make this hub perform.
//
// Deliberately per relay, never via nostr.WithRelayOptions at pool creation:
// the same pool also serves GetGeneralRelayUrls, which are public relays, and
// trusting those would let one of them inject forged events.
//
// Called repeatedly — the pool builds a fresh Relay on reconnect, which would
// otherwise silently lose the flag.
func (svc *service) applyTrustedRelayOptions(pool *nostr.SimplePool) {
	if !svc.cfg.TrustedNwcRelay() {
		return
	}

	for _, relayUrl := range svc.cfg.GetRelayUrls() {
		relay, ok := pool.Relays.Load(nostr.NormalizeURL(relayUrl))
		if !ok || relay == nil {
			continue
		}
		relay.AssumeValid = true
	}
}
