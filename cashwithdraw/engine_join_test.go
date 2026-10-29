package cashwithdraw

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
)

// joinedBatch commits a, pays it in a first version, then commits b, which
// joins the batch by replacement; it returns both payouts and both versions.
func joinedBatch(t *testing.T, w *engineWorld) (a, b *db.CashWithdrawal, v1, v2 db.CashWithdrawVersion) {
	t.Helper()
	a = w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	b = w.commitTo(t, 22_000_000)
	w.passAfterWindow(t)
	vs := w.versions(t)
	require.Len(t, vs, 2)
	b = w.payout(t, b.ID)
	require.NotNil(t, b.BatchID)
	require.Equal(t, vs[0].BatchID, *b.BatchID, "b joined a's batch")
	require.Equal(t, db.CashWithdrawBroadcast, b.State)
	return w.payout(t, a.ID), b, vs[0], vs[1]
}

// earlierWins makes the batch's first version confirm instead of the join:
// the network forgets v2 and mines v1.
func earlierWins(t *testing.T, w *engineWorld, v1, v2 db.CashWithdrawVersion) {
	t.Helper()
	w.chain.Evict(*v2.Txid)
	w.chain.Withhold(*v2.Txid, true)
	raw, err := hex.DecodeString(*v1.RawTx)
	require.NoError(t, err)
	require.NoError(t, w.chain.PublishTx(context.TODO(), raw))
	w.chain.Mine(1)
	w.pass(t)
	require.Equal(t, int64(1), w.chain.Confirmations(*v1.Txid))
}

// R2-P-H1: a payout that joined a batch by replacement, when an earlier version
// of the batch confirms instead, is paid in a new batch once that version is
// at final_depth; never stranded, never paid twice.
func TestR2_JoinedPayoutPaidAfterEarlierVersionConfirms(t *testing.T) {
	w := newEngineWorld(t, 5)
	base := w.conserved(t)
	a, b, v1, v2 := joinedBatch(t, w)
	earlierWins(t, w, v1, v2)

	// Until v1 is at final_depth, b stays in the batch: a reorganisation could
	// still confirm v2.
	require.Equal(t, *a.BatchID, *w.payout(t, b.ID).BatchID)

	for range 16 {
		w.chain.Mine(1)
		w.passAfterWindow(t)
	}
	gotA, gotB := w.payout(t, a.ID), w.payout(t, b.ID)
	require.Equal(t, db.CashWithdrawFinal, gotA.State)
	require.Equal(t, db.CashWithdrawFinal, gotB.State, "b is paid in a new batch")
	require.NotEqual(t, *a.BatchID, *gotB.BatchID)
	require.Equal(t, 1, w.paidCount(t, gotA))
	require.Equal(t, 1, w.paidCount(t, gotB))
	require.Nil(t, gotB.RemintAppID)
	require.Equal(t, base, w.conserved(t))

	// Both batches settle: nothing is polled forever.
	var open int64
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawBatch{}).Where("state IN ?",
		[]string{db.CashBatchOpen, db.CashBatchSigned}).Count(&open).Error)
	require.Zero(t, open)

	// The coins only v2 spent came back; no coin of the wallet is lost.
	var stuck int64
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawCoin{}).Where("state IN ?",
		[]string{db.CashCoinInVersion, db.CashCoinSetAside}).Count(&stuck).Error)
	require.Zero(t, stuck)
}

// R2-P-H1: a cancel request on the joined payout while the earlier version is
// confirming builds no invalidation spending already-spent inputs; at
// final_depth the payout is cancelled and re-minted whole, never paid.
func TestR2_JoinedPayoutCancelledAfterEarlierVersionConfirms(t *testing.T) {
	w := newEngineWorld(t, 5)
	base := w.conserved(t)
	a, b, v1, v2 := joinedBatch(t, w)
	earlierWins(t, w, v1, v2)

	w.mu.Lock()
	w.now = time.Unix(b.SentBy+604800, 0)
	w.mu.Unlock()
	_, err := w.s.CancelRequest(w.payout(t, b.ID))
	require.NoError(t, err)
	w.pass(t)
	require.Len(t, w.versions(t), 2, "no invalidation of a batch whose version confirmed")

	for range 8 {
		w.chain.Mine(1)
		w.pass(t)
	}
	gotA, gotB := w.payout(t, a.ID), w.payout(t, b.ID)
	require.Equal(t, db.CashWithdrawFinal, gotA.State)
	require.Equal(t, db.CashWithdrawCancelled, gotB.State)
	require.Equal(t, 0, w.paidCount(t, gotB))
	require.NotNil(t, gotB.RemintAppID)
	require.Equal(t, gotB.AmountMillis, balance(w.world, *gotB.RemintAppID))
	require.Len(t, w.versions(t), 2)
	require.Equal(t, base, w.conserved(t))
}

// R2-P-M1: a version whose signature failed stays about to sign only until the
// next pass, which signs it from its stored PSBT and broadcasts it; no restart
// is needed, and no second version is built for its payout.
func TestR2_FailedSignatureRetriedNextPass(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.chain.FailFinalize = 1
	w.advance(time.Duration(w.s.env().BatchWindowSecs()+1) * time.Second)
	require.Error(t, w.eng.Pass(context.TODO()), "the pass reports the failed signature")
	vs := w.versions(t)
	require.Len(t, vs, 1)
	require.Equal(t, db.CashVersionAboutToSign, vs[0].State)
	require.Nil(t, vs[0].RawTx)
	require.Equal(t, db.CashWithdrawQueued, w.payout(t, wd.ID).State)

	w.pass(t)
	vs = w.versions(t)
	require.Len(t, vs, 1, "the recorded version is signed again, not replaced")
	require.NotNil(t, vs[0].RawTx)
	got := w.payout(t, wd.ID)
	require.Equal(t, db.CashWithdrawBroadcast, got.State)
	require.Equal(t, *vs[0].Txid, *got.Txid)
	require.True(t, w.chain.InMempool(*got.Txid))
}

// R2-P-M3: a pass leases the coins a commit set aside before it reads the
// chain, so a chain read that fails leaves no set-aside coin unleased.
func TestR2_SetAsideLeasedBeforeChainRead(t *testing.T) {
	w := newEngineWorld(t, 5)
	a := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	require.NotNil(t, w.payout(t, a.ID).Txid, "a version exists, so the chain step reads the wallet")
	w.hooks.HoldSign.Store(true)
	b := w.commitTo(t, 22_000_000)
	var coins []db.CashWithdrawCoin
	require.NoError(t, w.svc.DB.Where("withdrawal_id = ? AND state = ?", b.ID, db.CashCoinSetAside).Find(&coins).Error)
	require.NotEmpty(t, coins)
	w.chain.WalletTxErr = errors.New("wallet busy")
	require.Error(t, w.eng.Pass(context.TODO()))
	id, err := w.eng.lease()
	require.NoError(t, err)
	for _, c := range coins {
		require.Positive(t, w.chain.LeaseOf(id, c.Outpoint), "leased although the chain read failed")
	}
}
