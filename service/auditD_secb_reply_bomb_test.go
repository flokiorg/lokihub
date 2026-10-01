package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// Session D, Security Auditor B, surface 2 -- reply bombs and chunk flooding.
//
// Two questions: what bounds the number of kind-23191 events the HUB will emit for one
// request, and what happens when a single result is too large to chunk at all. The second is
// the dangerous one, because chunkResults is called AFTER every item has run -- so a failure
// there means money may have moved and the caller gets nothing, not even a partial reply.

// auditDSecBMaximalRoster builds the largest cash_status result this codebase can produce:
// maxRecipientsPerWallet (100) rows, every optional field present, every identity_value at
// its maximum legal length (64 hex -- ValidateIdentityShape allows no other shape).
func auditDSecBMaximalRoster(t *testing.T, id string) transport.Result {
	t.Helper()
	const rows = 100 // cashwallet.maxRecipientsPerWallet
	ts := time.Now().Unix()
	recipients := make([]nipcash.RecipientStatus, rows)
	for i := range recipients {
		claimedAt := ts
		expiresAt := ts + 3600
		recipients[i] = nipcash.RecipientStatus{
			IdentityType:        "connection_key",
			IdentityValue:       strings.Repeat("f", 64),
			AmountMillis:        18446744073709551615,
			Claimed:             true,
			ClaimedAt:           &claimedAt,
			RedeemFeeMillis:     18446744073709551615,
			NetRedeemableMillis: 18446744073709551615,
			MinTransferMillis:   18446744073709551615,
			ExpiresAt:           &expiresAt,
		}
	}
	body, err := json.Marshal(nipcash.CashStatusResult{Recipients: recipients})
	require.NoError(t, err)
	return transport.Result{ID: id, ResultType: nipcash.MethodCashStatus, Result: body}
}

// TestAuditDSecB_MaximalSingleResultStillFitsOneChunk is the headroom question.
//
// chunkResults has one unrecoverable branch: "a single result for item %q does not fit the
// envelope limit". Reaching it loses the ENTIRE reply for an envelope whose items have already
// executed. This measures how far away that branch is.
func TestAuditDSecB_MaximalSingleResultStillFitsOneChunk(t *testing.T) {
	limits := transport.DefaultLimits()
	result := auditDSecBMaximalRoster(t, "max")

	chunks, err := chunkResults(strings.Repeat("ab", 32), []transport.Result{result}, limits)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	encoded, err := chunks[0].EncodeResponse(limits)
	require.NoError(t, err)

	t.Logf("largest producible single result: %d bytes of result body, %d bytes padded, limit %d (%.0f%% used)",
		len(result.Result), len(encoded), limits.MaxEnvelopeBytes,
		100*float64(len(encoded))/float64(limits.MaxEnvelopeBytes))

	assert.Less(t, len(encoded), limits.MaxEnvelopeBytes)
}

// TestAuditDSecB_OversizeResultGuardIsOrderDependent WAS the defect, and is now its
// regression. Fixed by also fit-testing an item when it STARTS a group, not only when it
// is the first item of the whole batch. The diagnosis below is kept verbatim because it
// is the clearest statement of what was wrong.
//
// chunkResults means to refuse an unfittable result up front -- "a single result for item %q
// does not fit the envelope limit" -- precisely so that a reply which cannot be delivered is
// never attempted for work already done. The guard only fires when the oversize result is the
// FIRST element of a group:
//
//	for _, r := range results {
//	    candidate := append(current, r)
//	    if !fits(candidate, len(results)) {
//	        if len(current) == 0 { return nil, err }   // <- only here
//	        groups = append(groups, current)
//	        current = []transport.Result{r}            // <- r is never fit-tested
//	        continue
//	    }
//	    current = candidate
//	}
//
// So an oversize result that follows a fitting one is closed into a group of its own and
// NEVER tested. chunkResults returns success, and the failure surfaces one layer up, in
// publishPrivateReply's EncodeResponse -- after the earlier chunks have already been
// published to the relay.
func TestAuditDSecB_OversizeResultGuardIsOrderDependent(t *testing.T) {
	limits := transport.DefaultLimits()
	nonce := strings.Repeat("ab", 32)

	huge, err := json.Marshal(map[string]string{"x": strings.Repeat("y", limits.MaxEnvelopeBytes)})
	require.NoError(t, err)
	oversize := transport.Result{ID: "too-big", ResultType: nipcash.MethodCashStatus, Result: huge}
	small := transport.Result{ID: "ok", ResultType: nipcash.MethodCashStatus,
		Result: json.RawMessage(`{"recipients":[]}`)}

	// First position: the guard fires, nothing is published, dispatchEnvelope logs and the
	// caller gets silence for work already done. Bad, but at least it is detected.
	chunks, err := chunkResults(nonce, []transport.Result{oversize, small}, limits)
	require.Error(t, err)
	assert.Nil(t, chunks)
	assert.ErrorContains(t, err, "does not fit the envelope limit")

	// Second position: the SAME result set, reordered, must be refused IDENTICALLY.
	// Pre-fix this returned (2 chunks, nil error) and chunk 2 could not be encoded — so
	// chunk 1 was published declaring Total: 2, the second never arrived, and the client
	// waited out the envelope's not_after before reporting ErrIncompleteReply for a
	// batch the hub had already executed.
	chunks, err = chunkResults(nonce, []transport.Result{small, oversize}, limits)
	require.Error(t, err, "the guard must not depend on where the oversize result sits")
	assert.Nil(t, chunks, "a reply that cannot be delivered whole must not be half-published")
	assert.ErrorContains(t, err, "does not fit the envelope limit")

	// And the refusal must not quote the id, which an attacker controls — see
	// transport.MaxItemIDBytes. Checked here because this error is the one that used to
	// write it into the hub's log.
	assert.NotContains(t, err.Error(), strings.Repeat("y", 64),
		"the error echoes attacker-controlled bytes into the log line that reports it")

	t.Logf("same results, both orderings refused identically: %v", err)
}

// TestAuditDSecB_ClientChosenItemIdIsEchoedWithNoLengthCap WAS the reachability path for
// the defect above — and it is what made it LIVE rather than latent, since no current
// method produces a result that large on its own. Now the regression for
// transport.MaxItemIDBytes.
//
// Envelope.check required only that an item id be non-empty — there was NO length cap —
// and transport.Result echoes the id verbatim. The request and the reply share one byte
// budget, but the reply carries every id AGAIN plus the result bodies, so an id that fits
// going in need not fit coming back. Measured before the fix: a request encoding to
// exactly 57344 of 57344 bytes, carrying one 48 KiB id, owed a reply of 83968 bytes.
//
// That is what made the order-dependence above LIVE rather than latent — no current
// method produces a single result that large by itself, but any client could make one by
// choosing its own item id.
//
// Fixed at the root, in nmilat: transport.MaxItemIDBytes bounds the id, so the
// asymmetry can no longer be reached by ids at all (32 items x 256 bytes is 8 KiB of a
// 56 KiB envelope).
func TestAuditDSecB_ClientChosenItemIdIsEchoedWithNoLengthCap(t *testing.T) {
	limits := transport.DefaultLimits()

	longID := strings.Repeat("z", 48*1024)
	env := transport.Envelope{
		Version:  transport.EnvelopeVersion,
		NotAfter: time.Now().Add(time.Minute).Unix(),
		Nonce:    strings.Repeat("ab", 32),
		ReplyTo:  strings.Repeat("cd", 32),
		Items: []transport.Item{{
			ID: longID, Target: strings.Repeat("cc", 32), Method: nipcash.MethodCashStatus,
			Params: json.RawMessage(`{}`), Proof: json.RawMessage(`{"kind":23192}`),
			BillProof: json.RawMessage(`{"kind":23193}`),
		}},
	}

	// Refused at encode, which is where the hub's own unwrap validates too — so the
	// oversize reply is now unreachable rather than merely handled.
	_, err := env.Encode(limits)
	require.Error(t, err, "a %d-byte item id must no longer be a legal request", len(longID))
	assert.ErrorContains(t, err, "over the 256 limit")

	// The refusal must not echo the id, or an attacker-chosen 48 KiB string lands in the
	// log line that reports it.
	assert.NotContains(t, err.Error(), strings.Repeat("z", 64),
		"the refusal quotes the very bytes it is refusing")

	// The boundary itself is usable: an id AT the cap is legal, so the fix bounds the
	// abuse without breaking a caller using long-but-sane ids.
	env.Items[0].ID = strings.Repeat("z", transport.MaxItemIDBytes)
	_, err = env.Encode(limits)
	require.NoError(t, err, "an id exactly at the cap must still be accepted")

	t.Logf("item id capped at %d bytes; pre-fix a 48 KiB id made a 57344-byte request owe an 83968-byte reply",
		transport.MaxItemIDBytes)
}

// TestAuditDSecB_HubChunkCountIsBoundedByItemCount answers "what bounds the hub".
func TestAuditDSecB_HubChunkCountIsBoundedByItemCount(t *testing.T) {
	limits := transport.DefaultLimits()

	results := make([]transport.Result, 0, limits.MaxItems)
	for i := 0; i < limits.MaxItems; i++ {
		results = append(results, auditDSecBMaximalRoster(t, "item"+string(rune('a'+i%26))+string(rune('a'+i/26))))
	}

	chunks, err := chunkResults(strings.Repeat("ab", 32), results, limits)
	require.NoError(t, err)

	wire := 0
	for _, c := range chunks {
		encoded, err := c.EncodeResponse(limits)
		require.NoError(t, err)
		wire += limits.EstimatedWireBytes() * 0 // keep the estimate out of the sum
		wire += (len(encoded) + 65 + 2) / 3 * 4 // base64 of plaintext + NIP-44 overhead
	}

	t.Logf("%d maximal results -> %d kind-23191 events, ~%d bytes of base64 published for one request",
		len(results), len(chunks), wire)
	t.Logf("the request that bought it was at most %d bytes of plaintext: ~%.0fx reply amplification",
		limits.MaxEnvelopeBytes, float64(wire)/float64(limits.MaxEnvelopeBytes))

	// The bound: never more chunks than results, and results are bounded by MaxItems.
	assert.LessOrEqual(t, len(chunks), len(results))
	assert.LessOrEqual(t, len(chunks), limits.MaxItems)
	for i, c := range chunks {
		assert.Equal(t, i+1, c.Seq)
		assert.Equal(t, len(chunks), c.Total)
	}
}

// TestAuditDSecB_ChunkResultsProbeCostIsQuadratic measures the packing loop.
//
// fits() re-encodes the whole candidate group on every step, and EncodeResponse pads with
// strings.Repeat up to MaxEnvelopeBytes -- so packing n small results performs O(n^2) result
// marshals and allocates a padding buffer for each probe. Bounded by MaxItems, so this is a
// cost note rather than a break; recorded with a number so nobody has to guess.
func TestAuditDSecB_ChunkResultsProbeCostIsQuadratic(t *testing.T) {
	limits := transport.DefaultLimits()
	nonce := strings.Repeat("ab", 32)

	small := func(id string) transport.Result {
		return transport.Result{ID: id, ResultType: nipcash.MethodCashStatus,
			Result: json.RawMessage(`{"recipients":[]}`)}
	}

	for _, n := range []int{1, 8, limits.MaxItems} {
		results := make([]transport.Result, 0, n)
		for i := 0; i < n; i++ {
			results = append(results, small("i"+string(rune('a'+i%26))+string(rune('a'+i/26))))
		}
		start := time.Now()
		const reps = 200
		for r := 0; r < reps; r++ {
			chunks, err := chunkResults(nonce, results, limits)
			require.NoError(t, err)
			require.Len(t, chunks, 1)
		}
		per := time.Since(start) / reps
		t.Logf("packing %2d small results: %s (expected probes: %d)", n, per, n*(n+1)/2)
	}
}

// TestAuditDSecB_EmptyResultSetStillProducesExactlyOneReply is the control for the junk case:
// an envelope whose every item was omitted -- which is what all of B-1's traffic looks like
// -- yields one small reply, not none and not many. So junk does NOT buy reply amplification.
// Recorded so the next round does not re-derive it.
func TestAuditDSecB_EmptyResultSetStillProducesExactlyOneReply(t *testing.T) {
	limits := transport.DefaultLimits()
	chunks, err := chunkResults(strings.Repeat("ab", 32), nil, limits)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, 1, chunks[0].Seq)
	assert.Equal(t, 1, chunks[0].Total)
	assert.Empty(t, chunks[0].Results)

	encoded, err := chunks[0].EncodeResponse(limits)
	require.NoError(t, err)
	t.Logf("an all-omitted envelope still costs the hub one published event of %d padded bytes", len(encoded))
	assert.Equal(t, limits.PadBucketBytes, len(encoded))
}
