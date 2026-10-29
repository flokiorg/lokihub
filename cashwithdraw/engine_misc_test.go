package cashwithdraw

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
)

// U-24.15: on postgres only one engine leads; a second takes over once the
// first lets go. sqlite runs one process and always leads.
func TestU_24_15_Leader(t *testing.T) {
	w := newEngineWorld(t, 1)
	e1, e2 := NewEngine(w.s), NewEngine(w.s)
	release1, ok, err := e1.TryLead(context.TODO())
	require.NoError(t, err)
	require.True(t, ok)
	if w.svc.DB.Name() != "postgres" {
		release1()
		t.Skip("leader election is postgres-only (TEST_DATABASE_URI); sqlite runs one process")
	}
	_, ok, err = e2.TryLead(context.TODO())
	require.NoError(t, err)
	assert.False(t, ok, "a second instance stays idle")
	release1()
	release2, ok, err := e2.TryLead(context.TODO())
	require.NoError(t, err)
	assert.True(t, ok, "and takes over")
	release2()
}

// U-24.18: a backend without lnclient.OnchainPayer cannot enable withdrawals;
// the engine never becomes ready and nothing is offered.
func TestU_24_18_NoCapability(t *testing.T) {
	w := newWorld(t)
	w.node.payer = nil
	e := NewEngine(w.s)
	assert.Error(t, e.Reconcile(context.TODO()))
	assert.False(t, e.Ready())
	assert.False(t, w.s.Offered(w.hub.ID))
	_, err := w.s.PutTerms(w.hub.ID, TermsInput{Enabled: true})
	assert.Error(t, err)
}

// The status endpoint's view.
func TestEngineStatus(t *testing.T) {
	w := newEngineWorld(t, 3)
	w.commitTo(t, 21_000_000)
	st, err := w.eng.Status()
	require.NoError(t, err)
	assert.True(t, st.Ready)
	assert.Equal(t, int64(1), st.Payouts["queued"])
	assert.Positive(t, st.UnconfirmedMillis)
	assert.Positive(t, st.SetAsideSat)
}

// NIP-CASH §Paying a Payout (CASH-87): a Hub broadcasts a version by sent_by,
// however long its batch window.
func TestEngine_BroadcastsBySentByWithALongBatchWindow(t *testing.T) {
	w := newEngineWorld(t, 5)
	w.svc.Cfg.GetEnv().CashWithdrawBatchWindowSecs = 6 * 3600
	wd := w.commitTo(t, 21_000_000)
	require.Greater(t, wd.SentBy, w.clock().Unix()+1800, "the quote leaves room before sent_by")

	w.advance(time.Duration(wd.SentBy-w.clock().Unix()-1800-60) * time.Second)
	w.pass(t)
	require.Nil(t, w.payout(t, wd.ID).Txid, "still batching well before sent_by")

	w.advance(time.Duration(wd.SentBy-w.clock().Unix()-1) * time.Second)
	w.pass(t)
	got := w.payout(t, wd.ID)
	require.Equal(t, db.CashWithdrawBroadcast, got.State, "broadcast before sent_by")
	require.LessOrEqual(t, w.clock().Unix(), got.SentBy)
}
