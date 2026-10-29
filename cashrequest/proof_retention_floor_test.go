package cashrequest_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashrequest"
)

// NIP-CASH §Claim Proofs (CASH-15): a consumed claim-proof id is kept at least
// until its created_at plus 6 minutes, past the window in which it still verifies.
func TestConsumedProof_KeptAtLeastSixMinutesPastCreatedAt(t *testing.T) {
	svc, s, now := newStore(t)
	ev := strings.Repeat("cd", 32)
	require.NoError(t, s.ConsumeProofTx(svc.DB, ev, now.Unix(), "cash_redeem"))
	for _, after := range []time.Duration{0, 359 * time.Second, 360 * time.Second, 361 * time.Second} {
		later := cashrequest.Store{DB: svc.DB, Now: func() time.Time { return now.Add(after) }}
		n, err := later.PruneProofs()
		require.NoError(t, err)
		assert.Zero(t, n, "pruned at created_at + %s", after)
		used, err := s.IsProofConsumed(ev)
		require.NoError(t, err)
		assert.True(t, used, "still consumed at created_at + %s", after)
	}
}

func TestConsumedProof_PrunedOnceTheRetentionHasPassed(t *testing.T) {
	svc, s, now := newStore(t)
	ev := strings.Repeat("ef", 32)
	require.NoError(t, s.ConsumeProofTx(svc.DB, ev, now.Unix(), "cash_redeem"))
	later := cashrequest.Store{DB: svc.DB, Now: func() time.Time {
		return now.Add(time.Duration(cashrequest.ProofRetentionSecs+1) * time.Second)
	}}
	n, err := later.PruneProofs()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}
