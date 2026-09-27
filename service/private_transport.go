package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"golang.org/x/sync/errgroup"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	nmilatnip01 "github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// privateTransportKeyIndex is the rotation index of the inbox key currently in
// use. Kept a constant for now: rotation needs the hub to keep accepting the
// previous key while bills in flight drain, and that grace set is not built yet.
// Wiring it to configuration before the grace set exists would advertise a
// capability the hub does not have.
const privateTransportKeyIndex = 0

// privateTransport holds what the receive path needs, resolved once at startup so
// the hot path never re-derives a key or re-reads config.
type privateTransport struct {
	// inboxPrivKey opens every inbound envelope. Held in process deliberately:
	// the node could not do this at a workable speed (602 µs per event, measured)
	// and flnd's ECDH returns a hashed value NIP-44 cannot consume.
	inboxPrivKey string
	// inboxXOnly is what clients p-tag and encrypt to, and what the relay filter
	// matches. A constant, so the filter never changes and no resubscribe is
	// needed when a bill is minted or destroyed.
	inboxXOnly string
	// nodeXOnly is the hub's LN identity in Nostr form — the key that signs the
	// announcement and the key a client recovers from a bill's mint signature.
	nodeXOnly string

	droppedEvents atomic.Int64
}

// minWrapBytes is a floor on an inbound envelope's ciphertext. Anything shorter
// cannot be a NIP-44 payload carrying even an empty envelope, so it is rejected
// without touching the decrypt path.
const minWrapBytes = 128

// newPrivateTransport resolves the inbox key and the node identity.
//
// The node identity is taken from the LN client's cached GetPubkey (compressed,
// 66 hex) and converted to x-only by dropping the prefix byte. That conversion is
// exact rather than lossy: BIP340 treats an x-only key as its even-Y lift, and
// since ECDH and schnorr verification both agree with that lift, the map from an
// LN identity to a Nostr identity is one-to-one.
func (svc *service) newPrivateTransport() (*privateTransport, error) {
	if svc.lnClient == nil {
		return nil, errors.New("private transport needs an LN client for the hub's node identity")
	}
	compressed := svc.lnClient.GetPubkey()
	if len(compressed) != 66 {
		return nil, fmt.Errorf("node pubkey is %d hex characters, want 66 (compressed)", len(compressed))
	}
	nodeXOnly := compressed[2:]

	inboxPrivKey, err := svc.keys.GetPrivateTransportKey(privateTransportKeyIndex)
	if err != nil {
		return nil, fmt.Errorf("derive private transport key: %w", err)
	}
	inboxXOnly, err := nostr.GetPublicKey(inboxPrivKey)
	if err != nil {
		return nil, fmt.Errorf("derive private transport pubkey: %w", err)
	}
	if inboxXOnly == nodeXOnly {
		// Would mean the hub cannot decrypt its own mail. Impossible with H+3
		// derivation, but the consequence is silence, so check rather than assume.
		return nil, errors.New("private transport inbox key collides with the node identity")
	}

	return &privateTransport{
		inboxPrivKey: inboxPrivKey,
		inboxXOnly:   inboxXOnly,
		nodeXOnly:    nodeXOnly,
	}, nil
}

// startPrivateTransport resolves the inbox key, announces it, and opens the
// subscription — in that order on purpose. Announcing before subscribing would
// leave a window where a client has been told where to write and nothing is
// reading; the reverse order costs nothing.
func (svc *service) startPrivateTransport(ctx context.Context, pool *nostr.SimplePool, group *errgroup.Group) error {
	pt, err := svc.newPrivateTransport()
	if err != nil {
		return err
	}

	group.Go(func() error {
		return svc.startPrivateTransportSubscription(ctx, pool, pt, group)
	})

	if err := svc.publishTransportAnnouncement(ctx, pool, pt); err != nil {
		// The subscription is already live, so the hub can serve anyone who
		// already knows the inbox key. Only newcomers are affected, which is why
		// this is not fatal — but it is an error, because an unannounced hub is
		// unreachable to every client that has not talked to it before.
		return fmt.Errorf("announcement failed, private transport is unreachable to new clients: %w", err)
	}
	return nil
}

// publishTransportAnnouncement tells clients which key to encrypt to.
//
// Signed by the LN node identity rather than by the inbox key, and that is the
// entire point: a client recovers the node identity from its bill's mint
// signature, so the node key is the only thing it can anchor trust to. An
// announcement signed by the inbox key would be vouching for itself, and anyone
// could publish one.
//
// Once per startup. The node signature costs ~1.15 ms, which is why this is not
// on any per-request path.
func (svc *service) publishTransportAnnouncement(ctx context.Context, pool *nostr.SimplePool, pt *privateTransport) error {
	signer, ok := svc.lnClient.(lnclient.TransportSigner)
	if !ok {
		return errors.New("this LN backend cannot sign the transport announcement (needs BIP340 over the node key)")
	}

	ev, err := transport.NewAnnouncement(
		pt.nodeXOnly, pt.inboxXOnly, svc.cfg.PrivateEnvelopeLimits(), svc.cfg.GetRelayUrls(),
	)
	if err != nil {
		return fmt.Errorf("build announcement: %w", err)
	}

	// The node hashes what it is given, so it must receive the event's
	// serialization, not its id — see AnnouncementSigningPayload. Handing over the
	// id yields a valid signature over the wrong digest and fails verification
	// with nothing indicating why.
	payload, err := transport.AnnouncementSigningPayload(ev)
	if err != nil {
		return fmt.Errorf("serialize announcement: %w", err)
	}
	sig, err := signer.SignSchnorrNodeKey(ctx, payload)
	if err != nil {
		return fmt.Errorf("node refused to sign the announcement: %w", err)
	}
	ev.Sig = hex.EncodeToString(sig)

	// Verify our own announcement before publishing it. A bad signature here is
	// unrecoverable in production: every client would reject it and conclude the
	// hub is unreachable, which is indistinguishable from the hub being down.
	if _, err := transport.ParseAnnouncement(ev, pt.nodeXOnly); err != nil {
		return fmt.Errorf("refusing to publish an announcement that does not verify: %w", err)
	}

	published, err := toGoNostrEvent(ev)
	if err != nil {
		return err
	}

	var publishedTo int
	for _, relayURL := range svc.cfg.GetRelayUrls() {
		relay, err := pool.EnsureRelay(relayURL)
		if err != nil {
			logger.Logger.Warn().Err(err).Str("relay", relayURL).
				Msg("Could not reach relay to publish the transport announcement")
			continue
		}
		if err := relay.Publish(ctx, *published); err != nil {
			logger.Logger.Warn().Err(err).Str("relay", relayURL).
				Msg("Failed to publish the transport announcement")
			continue
		}
		publishedTo++
	}
	if publishedTo == 0 {
		return errors.New("no relay accepted the transport announcement")
	}

	logger.Logger.Info().
		Str("inbox", pt.inboxXOnly).
		Str("node", pt.nodeXOnly).
		Int("relays", publishedTo).
		Msg("Published private transport announcement")
	return nil
}

// toGoNostrEvent converts nmilat's event into go-nostr's, which is what the pool
// publishes. Both are NIP-01 events; only the Go types differ.
func toGoNostrEvent(ev *nmilatnip01.Event) (*nostr.Event, error) {
	tags := make(nostr.Tags, 0, len(ev.Tags))
	for _, tag := range ev.Tags {
		tags = append(tags, nostr.Tag(tag))
	}
	return &nostr.Event{
		ID:        ev.ID,
		PubKey:    ev.PubKey,
		CreatedAt: nostr.Timestamp(ev.CreatedAt), //nolint:gosec // unix seconds
		Kind:      ev.Kind,
		Tags:      tags,
		Content:   ev.Content,
		Sig:       ev.Sig,
	}, nil
}

// startPrivateTransportSubscription opens the one subscription that serves every
// bill on the private transport.
//
// The filter is a single constant `p` value — the hub's inbox key — so unlike the
// per-wallet subscriptions it never changes: no resubscribe when a bill is minted
// or destroyed, and no window in which a freshly minted bill is unserved. That
// also means the relay does the first cut for us, which matters because matching
// here would otherwise cost a decryption attempt per event.
func (svc *service) startPrivateTransportSubscription(ctx context.Context, pool *nostr.SimplePool, pt *privateTransport, group *errgroup.Group) error {
	pt.logDroppedEventsPeriodically(ctx, group)

	filter := nostr.Filter{
		Kinds: []int{transport.KindPrivateRequest},
		Tags:  nostr.TagMap{"p": []string{pt.inboxXOnly}},
	}

	for {
		subCtx, cancelSubscription := context.WithCancel(ctx)
		eventsChannel := pool.SubscribeMany(subCtx, svc.cfg.GetRelayUrls(), filter)

		err := svc.watchPrivateSubscription(subCtx, eventsChannel, pt, group)
		cancelSubscription()

		if err != nil && ctx.Err() == nil {
			logger.Logger.Error().Err(err).
				Msg("got an error from the relay while listening to the private transport subscription, resubscribing")
			time.Sleep(3 * time.Second)
			continue
		}
		return nil
	}
}

// watchPrivateSubscription mirrors watchTrustedSubscription's lifecycle: the gate
// runs synchronously before any goroutine is spawned, so junk cannot make this hub
// schedule work.
//
// Note there is deliberately no `since` on the filter and none may be added: a
// wrapped event's created_at is randomised up to two days into the past, so a
// `since` would silently drop a fraction of legitimate traffic.
func (svc *service) watchPrivateSubscription(ctx context.Context, eventsChannel chan nostr.RelayEvent, pt *privateTransport, group *errgroup.Group) error {
	eventsChannelClosed := make(chan struct{}, 1)

	go func() {
		for event := range eventsChannel {
			select {
			case <-ctx.Done():
				return
			default:
				if !pt.acceptsPrivateEvent(event.Event) {
					continue
				}
				// TODO(stage5): unwrap, dedupe the envelope nonce and dispatch
				// each item. Until that lands the gate is exercised but nothing
				// is served, which is why the private kind is not advertised yet.
				logger.Logger.Debug().Str("id", event.Event.ID).
					Msg("Accepted a private transport envelope (handling not yet wired)")
			}
		}
		logger.Logger.Debug().Msg("Private transport subscription events channel ended")
		eventsChannelClosed <- struct{}{}
	}()

	select {
	case <-ctx.Done():
		return nil
	case <-eventsChannelClosed:
		return errors.New("private transport subscription exited abnormally")
	}
}

// acceptsPrivateEvent is the wire gate: allocation-free, no crypto, no database.
//
// It cannot do much, and that is inherent rather than a shortcoming. The inbox key
// is public — it is in an announcement on a relay — so anyone can address a
// well-formed event to it, and any test the hub could apply without its private
// key an attacker can satisfy. Discovering that an envelope is junk costs one ECDH
// (~166 µs measured), which is the intrinsic price of an anonymous inbox.
//
// So this rejects only what is cheap to reject, and the real defence is admission
// control at the relay: a per-session publish rate limit, with per-kind PoW held in
// reserve for when a hub is actually under attack.
func (pt *privateTransport) acceptsPrivateEvent(event *nostr.Event) bool {
	if event == nil || event.Kind != transport.KindPrivateRequest {
		pt.droppedEvents.Add(1)
		return false
	}
	// One string compare against a constant, not a map probe: there is exactly
	// one inbox key, unlike the standard path's registry of every wallet.
	if firstPTagValue(event) != pt.inboxXOnly {
		pt.droppedEvents.Add(1)
		return false
	}
	if len(event.Content) < minWrapBytes {
		pt.droppedEvents.Add(1)
		return false
	}
	return true
}

// logDroppedEventsPeriodically summarises what the gate discarded, on a timer
// rather than per event — formatting and IO on a path an attacker drives is itself
// a denial of service.
func (pt *privateTransport) logDroppedEventsPeriodically(ctx context.Context, group *errgroup.Group) {
	group.Go(func() error {
		ticker := time.NewTicker(droppedEventLogInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if dropped := pt.droppedEvents.Swap(0); dropped > 0 {
					logger.Logger.Info().
						Int64("dropped", dropped).
						Dur("interval", droppedEventLogInterval).
						Msg("Discarded events addressed to this hub's private transport inbox")
				}
			}
		}
	})
}
