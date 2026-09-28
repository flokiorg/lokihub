package service

import (
	"context"
	"fmt"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// dispatchEnvelope serves every item in one unwrapped envelope and publishes the reply.
//
// The per-item work lives in nip47.ServePrivateItem, where the controllers and their
// dependencies already are. What stays here is what only this layer knows: the envelope, the
// conversation key it arrived under, and the relays to answer on.
func (svc *service) dispatchEnvelope(
	ctx context.Context,
	pool *nostr.SimplePool,
	pt *privateTransport,
	lnClient lnclient.LNClient,
	envelope *transport.Envelope,
	conversationKey [32]byte,
) {
	binding := nip47.PrivateItemBinding{
		// The hub's IDENTITY key, which proofs bind to — not the inbox key the envelope
		// was encrypted to. Passing the wrong one here would reject every honest item.
		HubXOnly: pt.nodeXOnly,
		Nonce:    envelope.Nonce,
		NotAfter: envelope.NotAfter,
	}

	results := make([]transport.Result, 0, len(envelope.Items))
	for _, item := range envelope.Items {
		result, served := svc.nip47Service.ServePrivateItem(ctx, lnClient, item, binding)
		if !served {
			// Omitted, which is the protocol's answer for an item this hub cannot serve.
			// Counted rather than logged per item: an attacker can drive this path, and it
			// is also entirely normal for a client asking about bills it no longer holds.
			pt.omittedItems.Add(1)
			continue
		}
		results = append(results, result)
	}

	if err := svc.publishPrivateReply(ctx, pool, envelope, conversationKey, results); err != nil {
		// Error level because by now the items have RUN. For cash_redeem that means money
		// moved and the caller is about to see silence, which is the one outcome chunked
		// replies exist to avoid.
		logger.Logger.Error().Err(err).
			Int("items", len(envelope.Items)).
			Int("results", len(results)).
			Msg("Served a private envelope but could not deliver its reply")
	}
}

// publishPrivateReply encrypts and publishes the reply, splitting across events when one
// will not hold it.
func (svc *service) publishPrivateReply(
	ctx context.Context,
	pool *nostr.SimplePool,
	envelope *transport.Envelope,
	conversationKey [32]byte,
	results []transport.Result,
) error {
	limits := svc.cfg.PrivateEnvelopeLimits()
	replyKey, err := transport.DeriveReplyKey(conversationKey, envelope.ReplyTo)
	if err != nil {
		return fmt.Errorf("derive reply key: %w", err)
	}

	chunks, err := chunkResults(envelope.Nonce, results, limits)
	if err != nil {
		return err
	}

	for _, chunk := range chunks {
		plaintext, err := chunk.EncodeResponse(limits)
		if err != nil {
			return fmt.Errorf("encode reply %d/%d: %w", chunk.Seq, chunk.Total, err)
		}
		ciphertext, err := nip44.Encrypt(string(plaintext), replyKey)
		if err != nil {
			return fmt.Errorf("encrypt reply %d/%d: %w", chunk.Seq, chunk.Total, err)
		}

		// Authored by a FRESH ephemeral key, never the hub's identity. Signing with the
		// identity would leave a countable, permanent public record of how much
		// private-transport traffic this hub serves and when — undoing part of what the
		// transport is for. The signature does no authentication work anyway: only this hub
		// can derive the reply key, so decrypting is what proves authorship.
		ephemeral := nostr.GeneratePrivateKey()
		reply := nostr.Event{
			Kind:      transport.KindPrivateResponse,
			CreatedAt: nostr.Now(),
			// Addressed by the request's own reply_to (NIP-CASH §Addressing the Response).
			// That value appears nowhere else, so nothing on the relay links this reply to
			// the request it answers.
			Tags:    nostr.Tags{nostr.Tag{"p", envelope.ReplyTo}},
			Content: ciphertext,
		}
		if err := reply.Sign(ephemeral); err != nil {
			return fmt.Errorf("sign reply %d/%d: %w", chunk.Seq, chunk.Total, err)
		}

		published := false
		for _, relayURL := range svc.cfg.GetRelayUrls() {
			relay, err := pool.EnsureRelay(relayURL)
			if err != nil {
				continue
			}
			if err := relay.Publish(ctx, reply); err != nil {
				continue
			}
			published = true
			break
		}
		if !published {
			return fmt.Errorf("no relay accepted reply %d/%d", chunk.Seq, chunk.Total)
		}
	}
	return nil
}

// chunkResults splits results into as few replies as the limits allow.
//
// Sizing happens AFTER the items have run, because a hub cannot know how large a result will
// be before producing it: a cash_status roster grows with the bill's recipient count, and a
// cash_redeem result depends on what the payment did.
//
// Fit is tested by actually encoding rather than estimating, for the same reason the client
// packs that way — an estimate can disagree with the decoder, and being wrong here means a
// reply that cannot be delivered for work already done.
func chunkResults(reqNonce string, results []transport.Result, limits transport.Limits) ([]transport.ResponseEnvelope, error) {
	fits := func(rs []transport.Result, total int) bool {
		probe := transport.ResponseEnvelope{
			Version: transport.EnvelopeVersion, ReqNonce: reqNonce,
			Results: rs, Seq: 1, Total: total,
		}
		_, err := probe.EncodeResponse(limits)
		return err == nil
	}

	// An empty result set is still a reply. The client is waiting, and "every item was
	// omitted" is a legitimate answer it has to be able to observe.
	if len(results) == 0 {
		return []transport.ResponseEnvelope{{
			Version: transport.EnvelopeVersion, ReqNonce: reqNonce, Seq: 1, Total: 1,
		}}, nil
	}

	var groups [][]transport.Result
	current := []transport.Result{}
	for _, r := range results {
		candidate := append(append([]transport.Result(nil), current...), r)
		// Sized against len(results) as a provisional total: that is the largest the field
		// can become, so a group that fits here still fits once the real total is smaller.
		if !fits(candidate, len(results)) {
			if len(current) == 0 {
				return nil, fmt.Errorf("a single result for item %q does not fit the envelope limit", r.ID)
			}
			groups = append(groups, current)
			current = []transport.Result{r}
			continue
		}
		current = candidate
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}

	out := make([]transport.ResponseEnvelope, 0, len(groups))
	for i, g := range groups {
		out = append(out, transport.ResponseEnvelope{
			Version: transport.EnvelopeVersion, ReqNonce: reqNonce,
			Results: g, Seq: i + 1, Total: len(groups),
		})
	}
	return out, nil
}
