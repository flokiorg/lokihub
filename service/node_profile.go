package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

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

// PublishNodeProfile signs and publishes a kind-0 event for the hub's own
// node identity (the same nodeXOnly that signs the private-transport
// announcement) to the General relay pool — where every other profile in
// this app already lives, per GetGeneralRelayUrls's own doc comment.
//
// Deliberately independent of whether the private transport itself started:
// a hub that cannot (or has not yet) announced should still be able to
// publish a profile, so this re-derives the identity itself rather than
// reading it off a *privateTransport.
func (svc *service) PublishNodeProfile(ctx context.Context, metadata NodeProfileMetadata) error {
	if svc.lnClient == nil {
		return errors.New("publishing a node profile needs an LN client for the hub's node identity")
	}
	nodeXOnly, err := svc.nodeXOnlyIdentity()
	if err != nil {
		return err
	}

	signer, ok := svc.lnClient.(lnclient.TransportSigner)
	if !ok {
		return errors.New("this LN backend cannot sign a node profile (needs BIP340 over the node key)")
	}

	content, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal profile metadata: %w", err)
	}

	ev := nmilatnip01.NewUnsignedEvent(nostr.KindProfileMetadata, nodeXOnly, string(content))
	id, err := ev.HashID()
	if err != nil {
		return fmt.Errorf("hash profile event: %w", err)
	}
	ev.ID = hex.EncodeToString(id)

	// The node hashes what it is given, so it must receive the event's
	// serialization, not its id — same distinction
	// publishTransportAnnouncement's own comment makes: handing over the id
	// would sign sha256(id), a valid signature over the wrong digest.
	payload, err := ev.Serialize()
	if err != nil {
		return fmt.Errorf("serialize profile event: %w", err)
	}
	sig, err := signer.SignSchnorrNodeKey(ctx, payload)
	if err != nil {
		return fmt.Errorf("node refused to sign the profile: %w", err)
	}
	ev.Sig = hex.EncodeToString(sig)

	// Verify our own event before publishing it, same reasoning as the
	// announcement: a bad signature here is unrecoverable in production.
	if err := ev.Verify(nmilatnip01.WithoutPowCheck()); err != nil {
		return fmt.Errorf("refusing to publish a profile that does not verify: %w", err)
	}

	published, err := toGoNostrEvent(ev)
	if err != nil {
		return err
	}

	relayURLs := svc.cfg.GetGeneralRelayUrls()
	if len(relayURLs) == 0 {
		return errors.New("no general relays configured to publish the profile to")
	}

	// A fresh, short-lived pool: unlike the announcement (signed once at
	// startup, while the service's long-lived pool is already in scope),
	// publishing a profile is a rare, on-demand HTTP-triggered action with
	// no existing pool to reuse — svc holds no pool field past startup.
	pool := nostr.NewSimplePool(ctx)
	var publishedTo int
	for _, relayURL := range relayURLs {
		relay, relayErr := pool.EnsureRelay(relayURL)
		if relayErr != nil {
			logger.Logger.Warn().Err(relayErr).Str("relay", relayURL).
				Msg("Could not reach relay to publish the node profile")
			continue
		}
		if pubErr := relay.Publish(ctx, *published); pubErr != nil {
			logger.Logger.Warn().Err(pubErr).Str("relay", relayURL).
				Msg("Failed to publish the node profile")
			continue
		}
		publishedTo++
	}
	if publishedTo == 0 {
		return errors.New("no relay accepted the node profile")
	}

	logger.Logger.Info().
		Str("node", nodeXOnly).
		Int("relays", publishedTo).
		Msg("Published node profile")
	return nil
}
