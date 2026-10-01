package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Session D, Security Auditor B, surface 2 -- what one nonce slot actually costs a sender.
//
// transport.Envelope.Encode pads every envelope up to a PadBucketBytes boundary, so an
// HONEST client's smallest envelope is 8192 bytes of plaintext and ~11 KB of base64 on the
// wire. Decode does NOT require that padding: it checks a maximum size and nothing else. So
// the figure that bounds an attack is the unpadded minimum, which is what this measures.

// auditDSecBRawSealer seals arbitrary plaintext to the hub's inbox, bypassing Encode -- which
// is exactly what an attacker does, since Encode is a client-side courtesy.
func auditDSecBRawSealer(t *testing.T, pt *privateTransport) func(plaintext string) *nostr.Event {
	t.Helper()
	return func(plaintext string) *nostr.Event {
		t.Helper()
		ephemeralPriv := nostr.GeneratePrivateKey()
		ephemeralPub, err := nostr.GetPublicKey(ephemeralPriv)
		require.NoError(t, err)
		ck, err := nip44.GenerateConversationKey(pt.inboxXOnly, ephemeralPriv)
		require.NoError(t, err)
		ciphertext, err := nip44.Encrypt(plaintext, ck)
		require.NoError(t, err)
		return &nostr.Event{
			Kind:    transport.KindPrivateRequest,
			PubKey:  ephemeralPub,
			Tags:    nostr.Tags{nostr.Tag{"p", pt.inboxXOnly}},
			Content: ciphertext,
		}
	}
}

// auditDSecBMinimalEnvelope hand-writes the smallest JSON transport.Decode accepts: one item,
// both proofs present as minimal objects of the right kind, no pad field at all.
func auditDSecBMinimalEnvelope(nonce, replyTo string, notAfter int64) string {
	return fmt.Sprintf(
		`{"v":1,"not_after":%d,"nonce":"%s","reply_to":"%s","items":[{"id":"a","target":"%s","method":"cash_status","params":{},"proof":{"kind":23192},"bill_proof":{"kind":23193}}]}`,
		notAfter, nonce, replyTo, strings.Repeat("cc", 32))
}

// TestAuditDSecB_UnpaddedEnvelopeConsumesASlotAtATwentiethOfTheHonestCost is the finding.
//
// A padded envelope costs a sender ~11 KB per slot, which makes filling a 1<<20 set look like
// a 10 GB upload. Padding is not enforced on receipt, so the real price is the unpadded
// minimum -- and the ratio between the two is the whole gap between "needs a datacentre" and
// "needs a home connection".
func TestAuditDSecB_UnpaddedEnvelopeConsumesASlotAtATwentiethOfTheHonestCost(t *testing.T) {
	pt, seal := newUnwrapFixture(t)
	sealRaw := auditDSecBRawSealer(t, pt)
	limits := transport.DefaultLimits()

	// The honest client's floor, for comparison.
	honest := seal(t, testEnvelope(t))
	_, _, err := pt.unwrap(honest, limits, time.Now())
	require.NoError(t, err)

	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)
	plaintext := auditDSecBMinimalEnvelope(nonce, replyTo, time.Now().Add(time.Minute).Unix())

	ev := sealRaw(plaintext)
	require.True(t, pt.acceptsPrivateEvent(ev), "the wire gate's 128-byte floor does not catch it")

	got, _, err := pt.unwrap(ev, limits, time.Now())
	require.NoError(t, err, "an unpadded envelope is accepted: padding is a client-side courtesy")
	require.Equal(t, nonce, got.Nonce)
	require.Equal(t, int64(2), pt.nonces.Len(), "the slot was consumed")

	ratio := float64(len(honest.Content)) / float64(len(ev.Content))
	t.Logf("padded envelope: %d bytes plaintext -> %d bytes of event content", transport.DefaultPadBucketBytes, len(honest.Content))
	t.Logf("unpadded minimum: %d bytes plaintext -> %d bytes of event content (%.1fx cheaper)",
		len(plaintext), len(ev.Content), ratio)
	t.Logf("filling %d slots: %.2f GiB padded vs %.2f GiB unpadded",
		nonceSetCapacity,
		float64(len(honest.Content))*nonceSetCapacity/(1<<30),
		float64(len(ev.Content))*nonceSetCapacity/(1<<30))

	assert.Greater(t, ratio, 10.0, "padding is not enforced on receipt, so it bounds nothing")
}

// TestAuditDSecB_UnpaddedFloodFillsTheNonceSetFasterThanTheIngestLoopSaturates is the
// quantified version: throughput on the unpadded unit, and the bandwidth that sustains it.
//
// nonceSetCapacity's comment claims "an order of magnitude of headroom" over the live-entry
// count at saturation. That arithmetic was done against a 120 s slot lifetime. At 300 s the
// headroom is what this prints.
func TestAuditDSecB_UnpaddedFloodFillsTheNonceSetFasterThanTheIngestLoopSaturates(t *testing.T) {
	if testing.Short() {
		t.Skip("timing measurement")
	}
	pt, _ := newUnwrapFixture(t)
	sealRaw := auditDSecBRawSealer(t, pt)
	limits := transport.DefaultLimits()

	const samples = 3000
	events := make([]*nostr.Event, samples)
	notAfter := time.Now().Add(time.Minute).Unix()
	wireBytes := 0
	for i := range events {
		nonce, err := transport.NewNonce()
		require.NoError(t, err)
		replyTo, err := transport.NewNonce()
		require.NoError(t, err)
		events[i] = sealRaw(auditDSecBMinimalEnvelope(nonce, replyTo, notAfter))
		wireBytes += len(events[i].Content)
	}

	start := time.Now()
	for _, ev := range events {
		_, _, _ = pt.unwrap(ev, limits, time.Now())
	}
	elapsed := time.Since(start)
	require.Equal(t, int64(samples), pt.nonces.Len(), "every junk envelope took a slot")

	rate := float64(samples) / elapsed.Seconds()
	perSlotBytes := float64(wireBytes) / samples
	liveCeiling := rate * transport.ProofFreshnessPast.Seconds()
	headroom := liveCeiling / float64(nonceSetCapacity)
	bandwidth := rate * perSlotBytes

	t.Logf("unpadded junk: %s/envelope, %.0f envelopes/s on the single ingest goroutine", elapsed/samples, rate)
	t.Logf("live-entry ceiling over a %s slot lifetime: %.0f entries vs capacity %d -> %.2fx",
		transport.ProofFreshnessPast, liveCeiling, nonceSetCapacity, headroom)
	t.Logf("sustaining that costs the sender %.1f MB/s (%.0f Mbit/s); the set fills in %.0f s",
		bandwidth/1e6, bandwidth*8/1e6, float64(nonceSetCapacity)/rate)
	t.Logf("once full, the set drains only as entries age out: %s of refusals after the flood stops",
		transport.ProofFreshnessPast)

	assert.Greater(t, headroom, 1.0,
		"the capacity refusal is reachable before the ingest loop saturates")
	assert.Less(t, headroom, 10.0,
		"nonceSetCapacity's comment claims an order of magnitude of headroom")
}

// TestAuditDSecB_OperatorsPadBucketFloorMakesTheFloodCheaperStill is the operator half of the
// attacker model.
//
// MinPadBucketBytes (2 KiB) is a floor on an ANNOUNCED pad bucket, chosen to stop padding
// disclosing batch size. It does nothing for this attack in either direction, because the
// receive path never checks padding at all -- so no pad_bucket_bytes setting, floor or
// default, changes what a flood costs. Recorded so the next round does not look for a knob
// here.
func TestAuditDSecB_OperatorsPadBucketFloorMakesTheFloodCheaperStill(t *testing.T) {
	pt, _ := newUnwrapFixture(t)
	sealRaw := auditDSecBRawSealer(t, pt)

	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)
	plaintext := auditDSecBMinimalEnvelope(nonce, replyTo, time.Now().Add(time.Minute).Unix())

	for _, bucket := range []int{transport.MinPadBucketBytes, transport.DefaultPadBucketBytes, 32 * 1024} {
		limits := transport.DefaultLimits()
		limits.PadBucketBytes = bucket
		require.NoError(t, limits.Validate())

		fresh := newPrivateNonceSet()
		pt.nonces = fresh
		_, _, err := pt.unwrap(sealRaw(plaintext), limits, time.Now())
		require.NoError(t, err, "pad_bucket_bytes=%d still accepts a %d-byte unpadded envelope", bucket, len(plaintext))
		assert.Equal(t, int64(1), fresh.Len())
	}
	t.Logf("a %d-byte unpadded envelope is accepted at every legal pad_bucket_bytes: the receive path never checks padding",
		len(plaintext))
}
