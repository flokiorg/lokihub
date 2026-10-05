package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
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

	// nonces refuses replayed envelopes. See privateNonceSet for why it is in
	// memory rather than durable.
	nonces *privateNonceSet

	droppedEvents atomic.Int64
	// omittedItems counts items served envelopes chose not to answer — an unknown
	// target, a proof that did not verify, a method not on the allowlist. Counted
	// rather than logged per item because an attacker can drive this path, and because
	// it is also entirely normal traffic: a client asking about a bill it no longer
	// holds gets exactly this. A rising count against steady envelopes is the signal
	// worth watching, not any single occurrence.
	omittedItems atomic.Int64
	// rejectedEnvelopes counts envelopes that passed the wire gate but failed to
	// unwrap, decode, pass freshness or pass replay. Separate from droppedEvents
	// because these are the expensive rejections — each already cost an ECDH — so
	// the two numbers answer different operational questions.
	rejectedEnvelopes atomic.Int64
}

// PrivateTransportStatus is a point-in-time snapshot of the hub's
// private-transport announcement — for display (e.g. a Services page) and
// for diagnosing a client-reported ErrNoAnnouncement without reading hub
// logs. The zero value means the hub hasn't attempted to start the private
// transport yet.
type PrivateTransportStatus struct {
	NodeIdentity    string
	InboxPubkey     string
	Announced       bool
	AnnouncedRelays int
	TotalRelays     int
	// Error is the last publishTransportAnnouncement failure, if any. Empty
	// when Announced is true.
	Error     string
	UpdatedAt time.Time
}

// GetPrivateTransportStatus returns the hub's current private-transport
// status. Safe to call before startPrivateTransport has run (returns the
// zero value) and concurrently with it being updated.
func (svc *service) GetPrivateTransportStatus() PrivateTransportStatus {
	return svc.loadPrivateTransportStatus()
}

func (svc *service) loadPrivateTransportStatus() PrivateTransportStatus {
	if p := svc.privateTransportStatus.Load(); p != nil {
		return *p
	}
	return PrivateTransportStatus{}
}

// setPrivateTransportIdentity records the node/inbox identity as soon as
// it's resolved — before the announcement is attempted — so a failed publish
// still leaves the identity visible rather than the whole status blank.
func (svc *service) setPrivateTransportIdentity(nodeXOnly, inboxXOnly string) {
	status := svc.loadPrivateTransportStatus()
	status.NodeIdentity = nodeXOnly
	status.InboxPubkey = inboxXOnly
	svc.privateTransportStatus.Store(&status)
}

func (svc *service) setPrivateTransportAnnounceResult(announcedRelays, totalRelays int, announceErr error) {
	status := svc.loadPrivateTransportStatus()
	status.Announced = announceErr == nil
	status.AnnouncedRelays = announcedRelays
	status.TotalRelays = totalRelays
	if announceErr != nil {
		status.Error = announceErr.Error()
	} else {
		status.Error = ""
	}
	status.UpdatedAt = time.Now()
	svc.privateTransportStatus.Store(&status)
}

// minWrapBytes is a floor on an inbound envelope's ciphertext. Anything shorter
// cannot be a NIP-44 payload carrying even an empty envelope, so it is rejected
// without touching the decrypt path.
const minWrapBytes = 128

// nodeXOnlyIdentity derives the hub's Nostr-format identity from the LN
// node's cached GetPubkey (compressed, 66 hex) by dropping the prefix byte.
// That conversion is exact rather than lossy: BIP340 treats an x-only key as
// its even-Y lift, and since ECDH and schnorr verification both agree with
// that lift, the map from an LN identity to a Nostr identity is one-to-one.
//
// Shared by every caller that needs this identity (private-transport setup,
// node profile publishing) so they derive it identically. Callers are
// responsible for checking svc.lnClient is non-nil first, with their own
// context-appropriate error message.
func (svc *service) nodeXOnlyIdentity() (string, error) {
	compressed := svc.lnClient.GetPubkey()
	if len(compressed) != 66 {
		return "", fmt.Errorf("node pubkey is %d hex characters, want 66 (compressed)", len(compressed))
	}
	return compressed[2:], nil
}

// newPrivateTransport resolves the inbox key and the node identity.
func (svc *service) newPrivateTransport() (*privateTransport, error) {
	if svc.lnClient == nil {
		return nil, errors.New("private transport needs an LN client for the hub's node identity")
	}
	nodeXOnly, err := svc.nodeXOnlyIdentity()
	if err != nil {
		return nil, err
	}

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
		nonces:       newPrivateNonceSet(),
	}, nil
}

// unwrap opens one inbound event: decrypt, decode, check freshness, check replay.
//
// It returns the envelope and the conversation key, which the reply needs — the
// response is encrypted under a key derived from this same conversation key plus
// the envelope's reply_to, which is what saves an ECDH and a signature on the way
// back out.
//
// The order is cost-driven. Decryption is unavoidable and is the expensive step
// (~166 µs of ECDH, measured), so everything after it is arranged cheapest-first:
// structural decode, then freshness, then the replay check last. Freshness before
// replay specifically so a stale envelope cannot consume a slot in a bounded set.
func (pt *privateTransport) unwrap(event *nostr.Event, limits transport.Limits, now time.Time) (*transport.Envelope, [32]byte, error) {
	var conversationKey [32]byte

	// The sender is a fresh ephemeral key, which is why nothing here is
	// cacheable and why this cost is paid per event, junk included.
	conversationKey, err := nip44.GenerateConversationKey(event.PubKey, pt.inboxPrivKey)
	if err != nil {
		return nil, conversationKey, fmt.Errorf("conversation key: %w", err)
	}

	plaintext, err := nip44.Decrypt(event.Content, conversationKey)
	if err != nil {
		// Overwhelmingly the normal case for junk: the MAC fails. Not an error
		// worth a log line per occurrence — that is what the counter is for.
		return nil, conversationKey, fmt.Errorf("decrypt: %w", err)
	}

	envelope, err := transport.Decode([]byte(plaintext), limits)
	if err != nil {
		return nil, conversationKey, fmt.Errorf("decode: %w", err)
	}
	if err := envelope.CheckFreshness(now); err != nil {
		return nil, conversationKey, err
	}
	if pt.nonces.seenOrRecord(envelope.Nonce, envelope.NotAfter) {
		return nil, conversationKey, fmt.Errorf("envelope nonce %s… already seen", envelope.Nonce[:8])
	}
	return envelope, conversationKey, nil
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
	svc.setPrivateTransportIdentity(pt.nodeXOnly, pt.inboxXOnly)

	// Reclaim expired nonces so the set tracks the live window rather than
	// growing toward its cap, where it would start refusing real envelopes.
	pt.nonces.startNonceSweeper(ctx.Done())

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
func (svc *service) publishTransportAnnouncement(ctx context.Context, pool *nostr.SimplePool, pt *privateTransport) (err error) {
	relayURLs := svc.cfg.GetRelayUrls()
	var publishedTo int
	// Named return + defer so every exit path below — including the early
	// ones that never reach the publish loop — updates the retained status
	// the same way, instead of needing its own call before each return.
	defer func() {
		svc.setPrivateTransportAnnounceResult(publishedTo, len(relayURLs), err)
	}()

	signer, ok := svc.lnClient.(lnclient.TransportSigner)
	if !ok {
		err = errors.New("this LN backend cannot sign the transport announcement (needs BIP340 over the node key)")
		return err
	}

	ev, buildErr := transport.NewAnnouncement(
		pt.nodeXOnly, pt.inboxXOnly, svc.cfg.PrivateEnvelopeLimits(), relayURLs,
	)
	if buildErr != nil {
		err = fmt.Errorf("build announcement: %w", buildErr)
		return err
	}

	// The node hashes what it is given, so it must receive the event's
	// serialization, not its id — see AnnouncementSigningPayload. Handing over the
	// id yields a valid signature over the wrong digest and fails verification
	// with nothing indicating why.
	payload, payloadErr := transport.AnnouncementSigningPayload(ev)
	if payloadErr != nil {
		err = fmt.Errorf("serialize announcement: %w", payloadErr)
		return err
	}
	sig, signErr := signer.SignSchnorrNodeKey(ctx, payload)
	if signErr != nil {
		err = fmt.Errorf("node refused to sign the announcement: %w", signErr)
		return err
	}
	ev.Sig = hex.EncodeToString(sig)

	// Verify our own announcement before publishing it. A bad signature here is
	// unrecoverable in production: every client would reject it and conclude the
	// hub is unreachable, which is indistinguishable from the hub being down.
	if _, verifyErr := transport.ParseAnnouncement(ev, pt.nodeXOnly); verifyErr != nil {
		err = fmt.Errorf("refusing to publish an announcement that does not verify: %w", verifyErr)
		return err
	}

	published, convErr := toGoNostrEvent(ev)
	if convErr != nil {
		err = convErr
		return err
	}

	for _, relayURL := range relayURLs {
		relay, relayErr := pool.EnsureRelay(relayURL)
		if relayErr != nil {
			logger.Logger.Warn().Err(relayErr).Str("relay", relayURL).
				Msg("Could not reach relay to publish the transport announcement")
			continue
		}
		if pubErr := relay.Publish(ctx, *published); pubErr != nil {
			logger.Logger.Warn().Err(pubErr).Str("relay", relayURL).
				Msg("Failed to publish the transport announcement")
			continue
		}
		publishedTo++
	}
	if publishedTo == 0 {
		err = errors.New("no relay accepted the transport announcement")
		return err
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

		err := svc.watchPrivateSubscription(subCtx, pool, eventsChannel, pt, group)
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
func (svc *service) watchPrivateSubscription(ctx context.Context, pool *nostr.SimplePool, eventsChannel chan nostr.RelayEvent, pt *privateTransport, group *errgroup.Group) error {
	// Closed exactly once, by the drain goroutine below, on every exit path —
	// including ctx cancellation. That used to not be true: the drain
	// goroutine `return`ed directly on ctx.Done() without ever reaching its
	// final signal, so this function's own ctx.Done() case raced ahead and
	// returned while that goroutine (and whatever it was mid-logging or
	// mid-dispatching) was still running. Nothing downstream — including
	// nostrGroup.Wait() during shutdown, since the goroutine was also never
	// registered with `group` — actually waited for it.
	eventsChannelClosed := make(chan struct{})

	group.Go(func() error {
		defer close(eventsChannelClosed)
		for event := range eventsChannel {
			if ctx.Err() != nil {
				// Keep draining rather than returning early: the point is to
				// let eventsChannel close on its own (it does once every
				// per-relay goroutine notices this same ctx is done — see
				// SimplePool.subMany), so the `defer` above always fires
				// instead of racing this function's caller.
				continue
			}
			if !pt.acceptsPrivateEvent(event.Event) {
				continue
			}

			envelope, conversationKey, err := pt.unwrap(event.Event, svc.cfg.PrivateEnvelopeLimits(), time.Now())
			if err != nil {
				// Counted rather than logged per event: this path is one an
				// attacker drives, and formatting a message for each is
				// itself a denial of service. Debug for a working trail.
				pt.rejectedEnvelopes.Add(1)
				logger.Logger.Debug().Err(err).Str("id", event.Event.ID).
					Msg("Rejected a private transport envelope")
				continue
			}

			logger.Logger.Debug().
				Str("id", event.Event.ID).
				Int("items", len(envelope.Items)).
				Msg("Unwrapped a private transport envelope")

			// Dispatched on its own goroutine so one slow envelope — a redemption
			// waiting on a Lightning payment, say — cannot stall every envelope
			// queued behind it. Replay protection already happened inside unwrap,
			// so a duplicate cannot slip past while this one is still in flight.
			//
			// No loop-variable capture to worry about: envelope and conversationKey
			// are declared inside this iteration's body, not by the range clause.
			group.Go(func() error {
				svc.dispatchEnvelope(ctx, pool, pt, svc.lnClient, envelope, conversationKey)
				return nil
			})
		}
		logger.Logger.Debug().Msg("Private transport subscription events channel ended")
		return nil
	})

	// Always wait for the drain goroutine above to actually finish, whether
	// this generation ended because ctx was cancelled or because the relay
	// subscription died on its own. The two cases differ only in what we tell
	// the retry loop in startPrivateTransportSubscription afterwards.
	<-eventsChannelClosed
	if ctx.Err() != nil {
		return nil
	}
	return errors.New("private transport subscription exited abnormally")
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
