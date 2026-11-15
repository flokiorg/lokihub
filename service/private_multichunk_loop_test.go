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

	"github.com/flokiorg/lokihub/db"
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

		// A reply chunk holds up to NIP-44's ceiling (65535 bytes, NIP-CASH
		// §Addressing the Response), so the rosters must be large for twelve of them
		// to need several chunks: every bill gets 99 more recipients (100 is the cap).
		var bill db.App
		require.NoError(t, svc.DB.Where("wallet_pubkey = ?", target).First(&bill).Error)
		extra := make([]db.CashWalletClaim, 0, 99)
		for j := 0; j < 99; j++ {
			extra = append(extra, db.CashWalletClaim{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 1})
		}
		require.NoError(t, svc.AppsService.CreateCashWalletClaims(bill.ID, extra))
	}

	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)
	notAfter := time.Now().Add(time.Minute).Unix()

	binding := nipcash.ItemBinding{HubXOnly: pt.nodeXOnly, Nonce: nonce, NotAfter: notAfter}
	envelope := transport.Envelope{
		NotAfter: notAfter, Nonce: nonce, ReplyTo: replyTo,
	}
	for i := 0; i < bills; i++ {
		item, err := nipcash.StatusItem("bill"+string(rune('a'+i)), targets[i], connPrivs[i],
			nipcash.CashStatusParams{Scope: "all"}, nipcash.BySigning(privs[i]), binding)
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

	// The Hub's own policy: a reply is bounded by NIP-44's ceiling, not by max_bytes.
	replyLimits := requestLimits

	chunks, err := chunkResults(got.Nonce, results, replyLimits)
	require.NoError(t, err)
	require.Greater(t, len(chunks), 1,
		"these limits were chosen to force the multi-chunk branch; got %d chunk(s) — "+
			"enlarge the rosters rather than deleting the assertion, or this test "+
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

		// A client collecting a chunked reply reads each chunk with DecodeResponseChunk,
		// which allows NIP-44's ceiling: a reply pads past the request's max_bytes.
		decoded, err := transport.DecodeResponseChunk([]byte(opened), envelope.Nonce, requestedIDs, replyLimits.MaxEnvelopeBytes)
		require.NoError(t, err, "the SDK could not decode chunk %d of %d", chunk.Seq, chunk.Total)
		require.Empty(t, decoded.AlienIDs)
		require.Empty(t, decoded.DuplicateIDs)

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
		ReqNonce: got.Nonce,
		Results:  allResults, Seq: 1, Total: 1,
	}
	assert.Empty(t, reassembled.Omitted(requestedIDs),
		"Omitted must be empty once every chunk is in; a non-empty answer here is what a "+
			"caller reads as \"not served\" and may resend a spend on")

	t.Logf("%d bills answered across %d chunks under a %d-byte reply ceiling",
		bills, len(chunks), replyLimits.MaxEnvelopeBytes)
}
