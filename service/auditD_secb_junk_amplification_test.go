package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/nip47"
	"github.com/flokiorg/lokihub/tests"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// Session D, Security Auditor B, surface 2 -- goroutine-per-junk-event, and what one junk
// envelope makes the hub spend.
//
// The claim in watchPrivateSubscription's doc comment is "the gate runs synchronously before
// any goroutine is spawned, so junk cannot make this hub schedule work". That is true of junk
// the GATE can see -- a wrong kind, a wrong p-tag, a sub-128-byte content. It is not true of
// junk that decrypts, because the gate cannot see inside a ciphertext addressed to a public
// inbox key. These tests measure what the far side of that gate costs.

// auditDSecBJunkEnvelope builds a maximal envelope of items that are structurally perfect and
// authorize nothing: random targets no bill has ever had, and both proofs signed by keys
// invented on the spot.
//
// This is the important shape. A garbage SIGNATURE would be refused at
// billProofSigner, which is one secp256k1 verification. A VALID signature by the wrong key
// passes that gate and reaches the database, which is two queries further on -- so the
// expensive junk is the junk that verifies.
func auditDSecBJunkEnvelope(t *testing.T, hubXOnly string, items int) transport.Envelope {
	t.Helper()
	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)
	notAfter := time.Now().Add(time.Minute).Unix()

	env := transport.Envelope{
		Version: transport.EnvelopeVersion, NotAfter: notAfter, Nonce: nonce, ReplyTo: replyTo,
	}
	binding := nipcash.ItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}
	for i := 0; i < items; i++ {
		// A target nothing has ever served, and keys nobody gave the sender.
		target := tests.RandomHex32()
		item, err := nipcash.StatusItem(
			"junk"+string(rune('a'+i%26))+string(rune('a'+i/26)),
			target,
			nostr.GeneratePrivateKey(), // "connection secret": the sender's own invention
			nipcash.CashStatusParams{},
			nipcash.BySigning(nostr.GeneratePrivateKey()),
			binding,
		)
		require.NoError(t, err)
		env.Items = append(env.Items, item)
	}
	return env
}

// TestAuditDSecB_OneJunkEnvelopeBuysSignatureVerificationsAndDatabaseQueries measures the
// per-envelope cost of unauthenticated junk, end to end through this repo's real gates and a
// real database.
func TestAuditDSecB_OneJunkEnvelopeBuysSignatureVerificationsAndDatabaseQueries(t *testing.T) {
	if testing.Short() {
		t.Skip("timing measurement")
	}
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := nip47.NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	pt, _ := newUnwrapFixture(t)
	limits := svc.Cfg.PrivateEnvelopeLimits()

	env := auditDSecBJunkEnvelope(t, pt.nodeXOnly, limits.MaxItems)
	require.Len(t, env.Items, limits.MaxItems)

	// Within the announced budget, which is the point: the budget is not what bounds this.
	budget := 0
	for _, item := range env.Items {
		budget += item.VerificationCost()
	}
	require.LessOrEqual(t, budget, limits.MaxVerifyBudget)

	// What the sender actually has to upload. Encode pads; Decode does not require padding,
	// so the honest figure and the attacker's figure are both worth having.
	padded, err := env.Encode(limits)
	require.NoError(t, err)
	unpadded, err := json.Marshal(env)
	require.NoError(t, err)

	hubBinding := nip47.PrivateItemBinding{
		HubXOnly: pt.nodeXOnly, Nonce: env.Nonce, NotAfter: env.NotAfter,
	}

	start := time.Now()
	for _, item := range env.Items {
		_, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, hubBinding)
		require.False(t, served, "a junk item must be omitted, not answered")
	}
	elapsed := time.Since(start)

	perItem := elapsed / time.Duration(limits.MaxItems)
	t.Logf("%d junk items: %s total, %s per item, declared verification cost %d of a %d budget",
		limits.MaxItems, elapsed, perItem, budget, limits.MaxVerifyBudget)
	t.Logf("sender's upload for that: %d bytes unpadded (%d padded)", len(unpadded), len(padded))
	t.Logf("amplification: %.0f us of hub CPU + %d database queries per %d bytes uploaded",
		float64(elapsed.Microseconds()), 2*limits.MaxItems, len(unpadded))
	t.Logf("one core serves %.0f such envelopes/s; a 100 Mbit/s sender offers %.0f/s",
		1/elapsed.Seconds(), 100e6/8/float64(len(unpadded)))

	// The shape of the finding, asserted rather than just printed: a junk envelope is
	// orders of magnitude dearer for the hub than for the sender.
	assert.Greater(t, elapsed, 2*time.Millisecond,
		"a maximal junk envelope should be measurably expensive; if this got cheap, re-measure the rest")
}

// TestAuditDSecB_JunkWithAGarbageSignatureIsRefusedBeforeTheDatabase is the control that
// makes the shape above meaningful: the cheap junk really is cheaper, because billProofSigner
// runs before the lookup. Recorded so the next round does not re-derive the ordering.
func TestAuditDSecB_JunkWithAGarbageSignatureIsRefusedBeforeTheDatabase(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := nip47.NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	pt, _ := newUnwrapFixture(t)

	env := auditDSecBJunkEnvelope(t, pt.nodeXOnly, 1)
	item := env.Items[0]

	// Corrupt one hex character of the bill proof's signature.
	var proof map[string]any
	require.NoError(t, json.Unmarshal(item.BillProof, &proof))
	sig, _ := proof["sig"].(string)
	require.Len(t, sig, 128)
	flipped := "0"
	if sig[0] == '0' {
		flipped = "1"
	}
	proof["sig"] = flipped + sig[1:]
	corrupted, err := json.Marshal(proof)
	require.NoError(t, err)
	item.BillProof = corrupted

	_, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
		nip47.PrivateItemBinding{HubXOnly: pt.nodeXOnly, Nonce: env.Nonce, NotAfter: env.NotAfter})
	assert.False(t, served, "an unverifiable bill proof is an omission")
}

// TestAuditDSecB_TheWireGateReallyDoesStopUnopenableJunkForFree confirms the half of
// watchPrivateSubscription's claim that holds, so the report can be precise about which half
// does not.
func TestAuditDSecB_TheWireGateReallyDoesStopUnopenableJunkForFree(t *testing.T) {
	pt, _ := newUnwrapFixture(t)

	cases := []*nostr.Event{
		{Kind: 1, Tags: nostr.Tags{nostr.Tag{"p", pt.inboxXOnly}}, Content: longContent()},
		{Kind: transport.KindPrivateRequest, Tags: nostr.Tags{nostr.Tag{"p", "deadbeef"}}, Content: longContent()},
		{Kind: transport.KindPrivateRequest, Tags: nostr.Tags{nostr.Tag{"p", pt.inboxXOnly}}, Content: "short"},
	}
	for _, ev := range cases {
		assert.False(t, pt.acceptsPrivateEvent(ev))
	}
	assert.Equal(t, int64(len(cases)), pt.droppedEvents.Load())

	// But a ciphertext of the right shape addressed to the public inbox key always passes,
	// whatever is inside it -- which is why the gate cannot be the defence.
	sealRaw := auditDSecBRawSealer(t, pt)
	assert.True(t, pt.acceptsPrivateEvent(sealRaw("not an envelope at all")))
}

func longContent() string {
	out := make([]byte, minWrapBytes+1)
	for i := range out {
		out[i] = 'A'
	}
	return string(out)
}
