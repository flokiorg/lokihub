package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ohstr/nmilat/nipcash/transport"
)

// TestAuditAQA_ChunkResults_SeqAndTotalMatchTheChunksActuallyProduced closes the
// one property the hub's real chunker has never been asked about.
//
// chunkResults has no dedicated test: its only three callers are in
// private_full_loop_test.go, and all three produce a SINGLE chunk, so the
// multi-chunk branch of the hub's own chunker is never exercised. The client's
// reassembler IS tested for multi-chunk — against a FAKE hub
// (nmilat/nipcash/client/batch_e2e_test.go:471). So both halves are tested,
// each against the other's fake, which is the shape the full-loop test's own
// doc comment says it exists to rule out.
func TestAuditAQA_ChunkResults_SeqAndTotalMatchTheChunksActuallyProduced(t *testing.T) {
	limits := transport.Limits{
		MaxEnvelopeBytes:      16384,
		MaxItems:              32,
		PadBucketBytes:        512,
		MaxVerifyBudget:       200,
		MaxConsolidateSources: 2,
	}
	require.NoError(t, limits.Validate())

	nonce := strings.Repeat("ab", 32)

	// Results big enough that several cannot share one envelope.
	results := make([]transport.Result, 0, 8)
	for i := 0; i < 8; i++ {
		body, err := json.Marshal(map[string]string{"roster": strings.Repeat("x", 3000)})
		require.NoError(t, err)
		results = append(results, transport.Result{
			ID: fmt.Sprintf("i%d", i), ResultType: "cash_status", Result: body,
		})
	}

	chunks, err := chunkResults(nonce, results, limits)
	require.NoError(t, err)
	require.Greater(t, len(chunks), 1,
		"limits chosen so the multi-chunk branch runs; got %d chunk(s)", len(chunks))

	// The property: Total is the number of events the hub will actually publish,
	// and Seq enumerates them 1..Total with no gaps and no repeats.
	//
	// Mutation this catches, and which nothing else does:
	//   chunkResults, `Total: len(groups)` -> `Total: len(results)`
	// Every chunk still validates (1 <= Seq <= Total), every result still decodes,
	// and every existing assertion still passes -- but the client waits for results
	// that will never arrive and returns ErrIncompleteReply for work the hub has
	// already DONE. For cash_redeem that is a spend the caller cannot confirm.
	seen := map[int]bool{}
	var totalResults int
	for _, chunk := range chunks {
		require.Equal(t, len(chunks), chunk.Total,
			"chunk %d/%d: Total must equal the number of chunks published, not the number of results",
			chunk.Seq, chunk.Total)
		require.GreaterOrEqual(t, chunk.Seq, 1)
		require.LessOrEqual(t, chunk.Seq, len(chunks))
		require.False(t, seen[chunk.Seq], "Seq %d appears twice", chunk.Seq)
		seen[chunk.Seq] = true
		totalResults += len(chunk.Results)

		// Each chunk must be encodable under the same limits it was sized against.
		_, err := chunk.EncodeResponse(limits)
		require.NoError(t, err, "chunk %d/%d does not fit the limits it was sized for", chunk.Seq, chunk.Total)
	}
	require.Len(t, seen, len(chunks), "Seq must cover 1..Total with no gaps")
	require.Equal(t, len(results), totalResults, "no result may be dropped or duplicated across chunks")
}

// TestAuditAQA_ChunkResults_EmptyResultSetIsStillOneCompleteReply pins the
// "every item was omitted" reply, which a client must be able to observe as a
// COMPLETE answer rather than as a timeout.
func TestAuditAQA_ChunkResults_EmptyResultSetIsStillOneCompleteReply(t *testing.T) {
	limits := transport.DefaultLimits()
	nonce := strings.Repeat("cd", 32)

	chunks, err := chunkResults(nonce, nil, limits)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.Equal(t, 1, chunks[0].Seq)
	require.Equal(t, 1, chunks[0].Total)
	require.Empty(t, chunks[0].Results)

	encoded, err := chunks[0].EncodeResponse(limits)
	require.NoError(t, err)
	decoded, err := transport.DecodeResponse(encoded, nonce, nil, limits)
	require.NoError(t, err, "an all-omitted reply must decode as a complete reply")
	require.Empty(t, decoded.Results)
}
