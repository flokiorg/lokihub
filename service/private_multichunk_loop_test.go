package service

// The multi-chunk seam, carried open as A-O7 / C-O2.
//
// Session A's QA role closed "chunkResults has no test" and reached the multi-chunk
// branch — but its own doc comment names what was still missing: "The client's
// reassembler IS tested for multi-chunk — against a FAKE hub. So both halves are
// tested, each against the other's fake, which is the shape the full-loop test's own
// doc comment says it exists to rule out."
//
// That is this test. It is TestFullLoop_SDKBuildsRealHubServesSDKReads with limits
// tight enough to force several chunks, so the REAL hub chunker feeds the REAL
// client-side decoder across the real reply-key derivation and real NIP-44. No API
// change was needed in the end: the full-loop harness already crossed the seam, it just
// never produced more than one chunk.
//
// Modelling a hub with a small announced max_bytes rather than engineering enormous
// rosters: a hub chunks against what it announced and a client decodes against the
// same, so shrinking both is the faithful way to reach the branch. The REQUEST keeps
// the real limits, which is independent and is how a real exchange works — a hub's
// reply ceiling is its own.

import (
	"context"
	"testing"
	"time"

	gonip44 "github.com/nbd-wtf/go-nostr/nip44"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/nip47"
	"github.com/flokiorg/lokihub/tests"
)

func TestFullLoop_MultiChunkReply_RealHubChunkerRealClientDecoder(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := nip47.NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	pt, _ := newUnwrapFixture(t)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "multichunkfund")

	const bills = 12
	targets := make([]string, 0, bills)
	privs := make([]string, 0, bills)
	connPrivs := make([]string, 0, bills)
	for i := 0; i < bills; i++ {
		target, priv, connPriv := loopBill(t, svc, hub, "mc"+string(rune('a'+i)))
		targets = append(targets, target)
		privs = append(privs, priv)
		connPrivs = append(connPrivs, connPriv)
	}

	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)
	notAfter := time.Now().Add(time.Minute).Unix()

	binding := nipcash.ItemBinding{HubXOnly: pt.nodeXOnly, Nonce: nonce, NotAfter: notAfter}
	envelope := transport.Envelope{
		Version: transport.EnvelopeVersion, NotAfter: notAfter, Nonce: nonce, ReplyTo: replyTo,
	}
	for i := 0; i < bills; i++ {
		item, err := nipcash.StatusItem("bill"+string(rune('a'+i)), targets[i], connPrivs[i],
			nipcash.CashStatusParams{}, nipcash.BySigning(privs[i]), binding)
		require.NoError(t, err)
		envelope.Items = append(envelope.Items, item)
	}

	requestLimits := svc.Cfg.PrivateEnvelopeLimits()
	require.NoError(t, envelope.Validate(pt.nodeXOnly, time.Now()))
	plaintext, err := envelope.Encode(requestLimits)
	require.NoError(t, err)
	request, clientConversationKey, err := transport.WrapRequest(plaintext, pt.inboxXOnly)
	require.NoError(t, err)
	event, err := toGoNostrEvent(request)
	require.NoError(t, err)

	require.True(t, pt.acceptsPrivateEvent(event))
	got, hubConversationKey, err := pt.unwrap(event, requestLimits, time.Now())
	require.NoError(t, err)
	require.Len(t, got.Items, bills)

	results := make([]transport.Result, 0, bills)
	for _, item := range got.Items {
		result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
			nip47.PrivateItemBinding{HubXOnly: pt.nodeXOnly, Nonce: got.Nonce, NotAfter: got.NotAfter})
		require.True(t, served, "item %s was omitted; it is a real bill with a real proof", item.ID)
		results = append(results, result)
	}

	// A hub whose announced max_bytes cannot hold six rosters at once.
	replyLimits := requestLimits
	// 3072 is just above the SMALLEST LEGAL reply envelope, which is worth writing
	// down: MaxConsolidateSources has a hard floor of 2 (a one-source consolidate
	// means nothing), and EstimatedConsolidateItemBytes(2) is ~2914 bytes, so no hub
	// may announce a max_bytes below that. Both earlier attempts here were refused —
	// first for the ceiling rule, then for the two-source minimum.
	replyLimits.MaxEnvelopeBytes = 3072
	replyLimits.PadBucketBytes = 256
	// Lowered with it, and not incidentally: §Ceilings makes it a MUST that a hub not
	// announce a max_consolidate_sources its own max_bytes cannot hold, and
	// Limits.Validate enforces it — the first version of this test shrank max_bytes
	// alone and was correctly refused ("48 sources is ~46706 bytes, over the 1536
	// envelope limit"). A small hub announces small ceilings on BOTH.
	replyLimits.MaxConsolidateSources = 2
	require.NoError(t, replyLimits.Validate(),
		"the tightened reply policy must still be a legal one — this is a small hub, not a broken one")

	chunks, err := chunkResults(got.Nonce, results, replyLimits)
	require.NoError(t, err)
	require.Greater(t, len(chunks), 1,
		"these limits were chosen to force the multi-chunk branch; got %d chunk(s) — "+
			"retune MaxEnvelopeBytes rather than deleting the assertion, or this test "+
			"silently stops testing the thing it exists for", len(chunks))

	replyKey, err := transport.DeriveReplyKey(hubConversationKey, got.ReplyTo)
	require.NoError(t, err)
	clientReplyKey, err := transport.DeriveReplyKey(clientConversationKey, envelope.ReplyTo)
	require.NoError(t, err)

	requestedIDs := make([]string, 0, bills)
	for _, item := range envelope.Items {
		requestedIDs = append(requestedIDs, item.ID)
	}

	// The client's side of the contract, as a real one applies it: collect chunks until
	// `seen == total`, and treat the reply as complete only then. This is what makes a
	// wrong Total a money bug rather than a cosmetic one — the client would wait for a
	// chunk that never comes and report ErrIncompleteReply for work the hub has DONE.
	answered := map[string]bool{}
	seenSeq := map[int]bool{}
	declaredTotal := 0
	for _, chunk := range chunks {
		encoded, err := chunk.EncodeResponse(replyLimits)
		require.NoError(t, err)
		sealed, err := gonip44.Encrypt(string(encoded), replyKey)
		require.NoError(t, err)

		opened, err := gonip44.Decrypt(sealed, clientReplyKey)
		require.NoError(t, err, "the client could not decrypt chunk %d", chunk.Seq)

		decoded, err := transport.DecodeResponse([]byte(opened), envelope.Nonce, requestedIDs, replyLimits)
		require.NoError(t, err, "the SDK could not decode chunk %d of %d", chunk.Seq, chunk.Total)

		if declaredTotal == 0 {
			declaredTotal = decoded.Total
		}
		assert.Equal(t, declaredTotal, decoded.Total,
			"chunks disagree about Total; a client keyed on the first one would stop early or hang")
		assert.False(t, seenSeq[decoded.Seq], "Seq %d arrived twice", decoded.Seq)
		seenSeq[decoded.Seq] = true

		for _, r := range decoded.Results {
			require.NotNil(t, r.Result, "item %s came back with no roster", r.ID)
			assert.False(t, answered[r.ID], "item %s answered in two different chunks", r.ID)
			answered[r.ID] = true
		}
	}

	// The completeness rule, which is the whole point of Total.
	assert.Equal(t, declaredTotal, len(seenSeq),
		"the hub declared Total=%d but published %d chunks: a real client waits for the "+
			"difference, times out, and reports an incomplete reply for work that was done",
		declaredTotal, len(seenSeq))
	assert.Len(t, answered, bills, "every bill must come back exactly once across the chunks")
	for _, id := range requestedIDs {
		assert.True(t, answered[id], "item %s never came back", id)
	}

	// And nothing was omitted, per the SDK's own accounting.
	var allResults []transport.Result
	for _, chunk := range chunks {
		allResults = append(allResults, chunk.Results...)
	}
	reassembled := transport.ResponseEnvelope{
		Version: transport.EnvelopeVersion, ReqNonce: got.Nonce,
		Results: allResults, Seq: 1, Total: 1,
	}
	assert.Empty(t, reassembled.Omitted(requestedIDs),
		"Omitted must be empty once every chunk is in; a non-empty answer here is what a "+
			"caller reads as \"not served\" and may resend a spend on")

	t.Logf("%d bills answered across %d chunks under a %d-byte reply ceiling",
		bills, len(chunks), replyLimits.MaxEnvelopeBytes)
}
