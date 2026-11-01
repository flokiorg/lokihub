package notifications

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	nostrlsps5 "github.com/flowgate-lsp/nostr-lsps5"
	"github.com/nbd-wtf/go-nostr"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"
)

// NostrTransport implements NotificationTransport for Nostr encrypted LSPS5 delivery.
type NostrTransport struct {
	lspPrivKey string // hex private key
	lsps5      *nostrlsps5.LSPS5
	relayPool  *RelayPool
}

// NewNostrTransport creates a new Nostr transport
func NewNostrTransport(lspPrivKey string) *NostrTransport {
	return &NostrTransport{
		lspPrivKey: lspPrivKey,
		lsps5:      nostrlsps5.NewLSPS5(lspPrivKey),
		relayPool:  NewRelayPool(),
	}
}

// Type returns TransportTypeNostr
func (t *NostrTransport) Type() TransportType {
	return TransportTypeNostr
}

// Send delivers a notification via Nostr kind 31055.
// targetUri is the recipient's Nostr URI (nostr+lsps5://...).
func (t *NostrTransport) Send(ctx context.Context, targetUri string, method string, params interface{}) error {
	if t.lspPrivKey == "" {
		return fmt.Errorf("LSP Nostr private key not configured")
	}

	// 1. Parse URI
	targetPubkey, relays, err := nostrlsps5.ParseLsp5Uri(targetUri)
	if err != nil {
		return fmt.Errorf("invalid nostr URI: %v", err)
	}

	if len(relays) == 0 {
		return fmt.Errorf("no relays specified in URI")
	}

	log.Trace().
		Str("action", "parsed_uri").
		Str("target_pubkey", targetPubkey).
		Int("relays_count", len(relays)).
		Msg("NostrTransport: preparing event")

	// 2. Create Event (Pure Logic - returns unsigned)
	ev, err := t.lsps5.CreateNotificationEvent(targetPubkey, method, params)
	if err != nil {
		return fmt.Errorf("failed to create notification event: %v", err)
	}

	// 3. Sign the event (Transport responsibility)
	if err := ev.Sign(t.lspPrivKey); err != nil {
		return fmt.Errorf("failed to sign event: %v", err)
	}

	log.Trace().
		Str("action", "signed_event").
		Str("event_id", ev.ID).
		Msg("NostrTransport: event signed")

	// 4. Publish to Relays (Transport Logic)
	// Optimistic publish: Return success as soon as ONE relay accepts it.
	// We use a context cancel to stopping waiting for others (though we let them finish in bg usually).
	// Actually, better to just return early and let others finish or fail in background to maximize propagation without blocking caller.

	successChan := make(chan bool, len(relays))
	var wg sync.WaitGroup

	for _, url := range relays {
		wg.Add(1)
		go func(relayUrl string) {
			defer wg.Done()

			// Use separate context or same ctx?
			// If we return early, we don't want to cancel the connection attempt for others if we want redundancy.
			// But for latency, we just care about one.

			log.Trace().Str("relay", relayUrl).Msg("NostrTransport: connecting to relay")
			relay, err := t.relayPool.GetOrConnect(ctx, relayUrl)
			if err != nil {
				log.Warn().Err(err).Str("relay", relayUrl).Msg("Failed to connect to Nostr relay")
				// successChan <- false // Don't block
				return
			}

			// Publish
			err = relay.Publish(ctx, *ev)
			if err != nil {
				log.Warn().Err(err).Str("relay", relayUrl).Msg("Failed to publish to Nostr relay")
				return
			}

			log.Info().Str("relay", relayUrl).Str("method", method).Msg("Nostr: notification delivered")
			select {
			case successChan <- true:
			default:
			}
		}(url)
	}

	// Wait for first success or all done
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-successChan:
		// At least one succeeded
		return nil
	case <-done:
		// All finished, if we didn't receive success, check if we failed?
		// We only send to successChan on success.
		// If we are here, it means we didn't get a success yet?
		// Actually if buffered channel has item, select picks it.
		// If done closed, we check channel one last time.
		select {
		case <-successChan:
			return nil
		default:
			return fmt.Errorf("failed to publish to any relay")
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RelayPool manages persistent connections to relays
type RelayPool struct {
	relays sync.Map // map[string]*nostr.Relay
	// group serializes GetOrConnect per URL (F-6): without it, two
	// concurrent notifications to the same relay can both miss the cache,
	// both dial, and both Store — the second Store silently overwrites
	// the first, leaking a live connection that's never closed.
	group singleflight.Group
}

func NewRelayPool() *RelayPool {
	return &RelayPool{}
}

func (rp *RelayPool) GetOrConnect(ctx context.Context, url string) (*nostr.Relay, error) {
	v, err, _ := rp.group.Do(url, func() (interface{}, error) {
		if val, ok := rp.relays.Load(url); ok {
			relay := val.(*nostr.Relay)
			if relay.IsConnected() {
				return relay, nil
			}
			// relay.Connect() cannot revive a closed relay — the internal connectionContext
			// is permanently cancelled once close() is called. Create a fresh relay object.
			log.Info().Str("relay", url).Msg("NostrRelay: connection lost, reconnecting")
		} else {
			log.Info().Str("relay", url).Msg("NostrRelay: connecting")
		}

		connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		relay, err := nostr.RelayConnect(connectCtx, url)
		if err != nil {
			return nil, fmt.Errorf("failed to reconnect to relay: %w", err)
		}
		rp.relays.Store(url, relay)
		return relay, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*nostr.Relay), nil
}

// Publish sends ev to the relay at url through the pool's connection and
// waits for the relay's OK. It is how NIP-LSP announcements are published
// (T8-3).
func (rp *RelayPool) Publish(ctx context.Context, url string, ev nostr.Event) error {
	relay, err := rp.GetOrConnect(ctx, url)
	if err != nil {
		return err
	}
	return relay.Publish(ctx, ev)
}

// RelayList asks each relay for pubkey's NIP-65 relay list (kind 10002) and
// returns the newest validly signed one, or nil when none has one. It fails
// only when no relay answered.
func (rp *RelayPool) RelayList(ctx context.Context, relays []string, pubkey string) (*nostr.Event, error) {
	filter := nostr.Filter{Kinds: []int{nostr.KindRelayListMetadata}, Authors: []string{pubkey}, Limit: 1}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		newest   *nostr.Event
		answered bool
		errs     []error
	)
	for _, url := range relays {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			relay, err := rp.GetOrConnect(ctx, url)
			var evs []*nostr.Event
			if err == nil {
				evs, err = relay.QuerySync(ctx, filter)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", url, err))
				return
			}
			answered = true
			for _, ev := range evs {
				if ev.Kind != nostr.KindRelayListMetadata || ev.PubKey != pubkey {
					continue
				}
				if ok, err := ev.CheckSignature(); err != nil || !ok {
					continue
				}
				if newest == nil || ev.CreatedAt > newest.CreatedAt {
					newest = ev
				}
			}
		}(url)
	}
	wg.Wait()
	if !answered && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return newest, nil
}
