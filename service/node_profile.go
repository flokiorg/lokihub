package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	nmilatnip01 "github.com/ohstr/nmilat/nip01"
)

// NodeProfileMetadata is a kind-0 profile in full. Kind 0 is replaceable, so
// publishing always replaces the whole thing — there is no partial-update
// path, and a caller that wants to keep an existing field must resend it.
type NodeProfileMetadata struct {
	Name    string `json:"name,omitempty"`
	About   string `json:"about,omitempty"`
	Picture string `json:"picture,omitempty"`
	Banner  string `json:"banner,omitempty"`
	Nip05   string `json:"nip05,omitempty"`
	Lud16   string `json:"lud16,omitempty"`
}

// PublishedNodeEvent is what a caller needs to show the just-published
// event immediately (e.g. prepend it into a notes feed) without waiting on
// relay propagation/outbox indexing to read it back.
type PublishedNodeEvent struct {
	ID        string
	CreatedAt int64
}

// PublishNodeProfile signs and publishes a kind-0 event for the hub's own
// node identity to the General relay pool — where every other profile in
// this app already lives, per GetGeneralRelayUrls's own doc comment.
func (svc *service) PublishNodeProfile(ctx context.Context, metadata NodeProfileMetadata) error {
	content, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal profile metadata: %w", err)
	}
	_, err = svc.publishNodeEvent(ctx, nostr.KindProfileMetadata, string(content))
	return err
}

// PublishNodeNote signs and publishes a kind-1 text note for the hub's own
// node identity, same relay pool and identity as PublishNodeProfile. First
// version: plain text only, no media/attachments.
func (svc *service) PublishNodeNote(ctx context.Context, content string) (*PublishedNodeEvent, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, errors.New("note content cannot be empty")
	}
	return svc.publishNodeEvent(ctx, nostr.KindTextNote, content)
}

// publishNodeEvent builds, signs, verifies, converts and publishes an
// arbitrary nip01 event authored by the hub's own node identity — the
// generic core PublishNodeProfile and PublishNodeNote both share. Always
// signed with nodeXOnlyIdentity() (the LN node's own key, recoverable from
// a bill's mint signature and the same key the private-transport
// announcement signs with) — never the private-transport inbox key
// (*privateTransport.inboxXOnly), which this function never even has
// access to: it is scoped to *privateTransport, not *service, and nothing
// here reads it. Deliberately independent of whether the private transport
// itself started: a hub that cannot (or has not yet) announced should still
// be able to publish a profile or a note, so this re-derives the identity
// itself rather than reading it off a *privateTransport.
func (svc *service) publishNodeEvent(ctx context.Context, kind int, content string) (*PublishedNodeEvent, error) {
	if svc.lnClient == nil {
		return nil, errors.New("publishing a node event needs an LN client for the hub's node identity")
	}
	nodeXOnly, err := svc.nodeXOnlyIdentity()
	if err != nil {
		return nil, err
	}

	signer, ok := svc.lnClient.(lnclient.TransportSigner)
	if !ok {
		return nil, errors.New("this LN backend cannot sign a node event (needs BIP340 over the node key)")
	}

	ev := nmilatnip01.NewUnsignedEvent(kind, nodeXOnly, content)
	id, err := ev.HashID()
	if err != nil {
		return nil, fmt.Errorf("hash node event: %w", err)
	}
	ev.ID = hex.EncodeToString(id)

	// The node hashes what it is given, so it must receive the event's
	// serialization, not its id — same distinction
	// publishTransportAnnouncement's own comment makes: handing over the id
	// would sign sha256(id), a valid signature over the wrong digest.
	payload, err := ev.Serialize()
	if err != nil {
		return nil, fmt.Errorf("serialize node event: %w", err)
	}
	sig, err := signer.SignSchnorrNodeKey(ctx, payload)
	if err != nil {
		return nil, fmt.Errorf("node refused to sign the event: %w", err)
	}
	ev.Sig = hex.EncodeToString(sig)

	// Verify our own event before publishing it, same reasoning as the
	// announcement: a bad signature here is unrecoverable in production.
	if err := ev.Verify(nmilatnip01.WithoutPowCheck()); err != nil {
		return nil, fmt.Errorf("refusing to publish an event that does not verify: %w", err)
	}

	published, err := toGoNostrEvent(ev)
	if err != nil {
		return nil, err
	}

	relayURLs := svc.cfg.GetGeneralRelayUrls()
	if len(relayURLs) == 0 {
		return nil, errors.New("no general relays configured to publish to")
	}

	// A fresh, short-lived pool: unlike the announcement (signed once at
	// startup, while the service's long-lived pool is already in scope),
	// publishing here is a rare, on-demand HTTP-triggered action with no
	// existing pool to reuse — svc holds no pool field past startup.
	pool := nostr.NewSimplePool(ctx)
	var publishedTo int
	for _, relayURL := range relayURLs {
		relay, relayErr := pool.EnsureRelay(relayURL)
		if relayErr != nil {
			logger.Logger.Warn().Err(relayErr).Str("relay", relayURL).
				Msg("Could not reach relay to publish the node event")
			continue
		}
		if pubErr := relay.Publish(ctx, *published); pubErr != nil {
			logger.Logger.Warn().Err(pubErr).Str("relay", relayURL).
				Msg("Failed to publish the node event")
			continue
		}
		publishedTo++
	}
	if publishedTo == 0 {
		return nil, errors.New("no relay accepted the node event")
	}

	logger.Logger.Info().
		Str("node", nodeXOnly).
		Int("kind", kind).
		Int("relays", publishedTo).
		Msg("Published node event")

	return &PublishedNodeEvent{ID: ev.ID, CreatedAt: int64(ev.CreatedAt)}, nil //nolint:gosec // CreatedAt is a unix timestamp in seconds, nowhere near MaxInt64
}
