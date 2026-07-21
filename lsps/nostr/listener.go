package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/logger"
	nostrlsps5 "github.com/flowgate-lsp/nostr-lsps5"
	"github.com/nbd-wtf/go-nostr"
)

// nostrPool is the subset of *nostr.SimplePool used by Listener, allowing a
// test double to be injected without depending on the concrete type.
// The return type matches SimplePool.SubscribeMany exactly (bidirectional chan).
type nostrPool interface {
	SubscribeMany(ctx context.Context, urls []string, filter nostr.Filter, opts ...nostr.SubscriptionOption) chan nostr.RelayEvent
}

// resubscribeDelay is the backoff between a relay channel close and the next
// SubscribeMany call. Mirrors streamRetryDelay in lsps/manager.
const resubscribeDelay = 5 * time.Second

// Listener handles incoming LSPS5 notifications over Nostr
type Listener struct {
	keys              keys.Keys
	cfg               config.Config
	pool              nostrPool
	eventPublisher    events.EventPublisher
	relays            []string
	mu                sync.Mutex
	stop              chan struct{}
	getTrustedPubkeys func() []string
	lsps5             *nostrlsps5.LSPS5
}

func NewListener(keys keys.Keys, cfg config.Config, eventPublisher events.EventPublisher, getTrustedPubkeys func() []string) *Listener {
	lsps5 := nostrlsps5.NewLSPS5(keys.GetNostrSecretKey())
	return &Listener{
		keys:              keys,
		cfg:               cfg,
		eventPublisher:    eventPublisher,
		stop:              make(chan struct{}),
		getTrustedPubkeys: getTrustedPubkeys,
		lsps5:             lsps5,
	}
}

// Start connects to relays and subscribes to notifications
func (l *Listener) Start(ctx context.Context, pool *nostr.SimplePool) error {
	// Use injected pool if provided
	if pool != nil {
		l.pool = pool
	}

	if l.pool == nil {
		return fmt.Errorf("nostr pool not initialized")
	}

	// Get relays from config
	l.relays = l.cfg.GetRelayUrls()
	if len(l.relays) == 0 {
		logger.Logger.Warn().Msg("No relays configured for LSPS5 Nostr listener")
		return nil
	}

	pubkey := l.keys.GetNostrPublicKey()
	if pubkey == "" {
		return fmt.Errorf("nostr public key not available")
	}

	logger.Logger.Info().
		Str("pubkey", pubkey).
		Interface("relays", l.relays).
		Msg("Starting LSPS5 Nostr listener")

	filters := nostr.Filter{
		Kinds: []int{nostrlsps5.KindLSPS5},
		Tags: nostr.TagMap{
			"p": []string{pubkey},
			"t": []string{nostrlsps5.TagLSPS5},
		},
		Since: ptr(nostr.Timestamp(time.Now().Add(-24 * time.Hour).Unix())),
	}

	go l.runSubscriptionLoop(ctx, filters)

	return nil
}

// runSubscriptionLoop reads relay events and resubscribes whenever the channel
// closes (relay disconnect). Exits on context cancellation or Stop().
func (l *Listener) runSubscriptionLoop(ctx context.Context, filters nostr.Filter) {
	currentSub := l.pool.SubscribeMany(ctx, l.relays, filters)
	for {
		select {
		case ev, ok := <-currentSub:
			if !ok {
				// Relay subscription closed (relay disconnect or CLOSED message).
				// Back off before resubscribing to avoid a tight reconnect loop
				// under a misbehaving relay sending rapid CLOSE frames.
				logger.Logger.Warn().Msg("LSPS5 Nostr relay subscription closed, resubscribing")
				select {
				case <-ctx.Done():
					return
				case <-l.stop:
					return
				case <-time.After(resubscribeDelay):
				}
				filters.Since = ptr(nostr.Timestamp(time.Now().Unix()))
				currentSub = l.pool.SubscribeMany(ctx, l.relays, filters)
				continue
			}
			if ev.Event == nil {
				continue
			}
			// Pre-filter: drop untrusted events before spawning a goroutine to
			// prevent goroutine accumulation under an event flood.
			if !l.isTrustedPubkey(ev.Event.PubKey) {
				logger.Logger.Debug().Str("pubkey", ev.Event.PubKey).
					Msg("Dropping LSPS5 event from untrusted source")
				continue
			}
			go l.handleEvent(ctx, ev.Event)
		case <-ctx.Done():
			return
		case <-l.stop:
			return
		}
	}
}

// isTrustedPubkey reports whether pubkey is in the current trusted-LSP set.
func (l *Listener) isTrustedPubkey(pubkey string) bool {
	if l.getTrustedPubkeys == nil {
		return false
	}
	for _, p := range l.getTrustedPubkeys() {
		if p == pubkey {
			return true
		}
	}
	return false
}

func (l *Listener) Stop() {
	close(l.stop)
}

func (l *Listener) handleEvent(ctx context.Context, ev *nostr.Event) {
	// Verify sender identity against LSP allowlist
	trusted := false
	if l.getTrustedPubkeys != nil {
		trustedPubkeys := l.getTrustedPubkeys()
		for _, pub := range trustedPubkeys {
			if ev.PubKey == pub {
				trusted = true
				break
			}
		}
	} else {
		logger.Logger.Warn().Msg("No trusted pubkey validator provided, ignoring event")
		return
	}

	if !trusted {
		logger.Logger.Warn().Str("pubkey", ev.PubKey).Msg("Received LSPS5 event from untrusted source")
		return
	}

	// Verify event signature
	if ok, err := ev.CheckSignature(); !ok || err != nil {
		logger.Logger.Warn().Str("id", ev.ID).Msg("Invalid signature on LSPS5 Nostr event")
		return
	}

	// Parse notification via library
	notification, err := l.lsps5.ParseNotificationEvent(ev, ev.PubKey)
	if err != nil {
		logger.Logger.Error().Err(err).Str("id", ev.ID).Msg("Failed to parse LSPS5 notification via library")
		return
	}

	// Dispatch internal event
	event := &events.Event{
		Event: constants.LSPS5_EVENT_NOTIFICATION,
		Properties: map[string]interface{}{
			"lsp_pubkey": ev.PubKey,
			"method":     notification.Method,
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
			"transport":  "nostr",
		},
	}

	switch notification.Method {
	case constants.LSPS5_EVENT_PAYMENT_INCOMING:
		event.Event = constants.LSPS5_EVENT_PAYMENT_INCOMING
		logger.Logger.Info().Str("lsp", ev.PubKey).Msg("Received payment incoming via Nostr")

	case constants.LSPS5_EVENT_EXPIRY_SOON:
		event.Event = constants.LSPS5_EVENT_EXPIRY_SOON
		// Parse params
		var params struct {
			Timeout uint32 `json:"timeout"`
		}

		// Helper to marshal params back to bytes then unmarshal
		paramsBytes, err := json.Marshal(notification.Params)
		if err == nil {
			if err := json.Unmarshal(paramsBytes, &params); err == nil {
				if props, ok := event.Properties.(map[string]interface{}); ok {
					props["timeout_block"] = params.Timeout
				}
			}
		}

	case constants.LSPS5_EVENT_ORDER_STATE_CHANGED:
		event.Event = constants.LSPS5_EVENT_ORDER_STATE_NOTIFICATION
		var params struct {
			OrderID      string  `json:"order_id"`
			State        string  `json:"state"`
			ChannelPoint *string `json:"channel_point,omitempty"`
			Error        *string `json:"error,omitempty"`
		}

		paramsBytes, err := json.Marshal(notification.Params)
		if err == nil {
			if err := json.Unmarshal(paramsBytes, &params); err == nil {
				if props, ok := event.Properties.(map[string]interface{}); ok {
					props["order_id"] = params.OrderID
					props["state"] = params.State
					if params.ChannelPoint != nil {
						props["channel_point"] = *params.ChannelPoint
					}
					if params.Error != nil {
						props["error"] = *params.Error
					}
				}
			}
		}
		logger.Logger.Info().
			Str("lsp", ev.PubKey).
			Str("state", params.State).
			Msg("Order state changed notification via Nostr")
	}

	l.eventPublisher.Publish(event)
}

func ptr[T any](v T) *T {
	return &v
}
