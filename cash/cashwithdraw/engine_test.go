package cashwithdraw

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
)

// The happy path: a committed payout is batched, signed after its
// about-to-sign record, broadcast, confirmed and final at final_depth.
func TestEngine_PayToFinal(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	wd = w.payout(t, wd.ID)
	require.Equal(t, db.CashWithdrawBroadcast, wd.State)
	assert.True(t, wd.Signed)
	require.NotNil(t, wd.Txid)
	assert.True(t, w.chain.InMempool(*wd.Txid))

	w.chain.Mine(1)
	w.pass(t)
	wd = w.payout(t, wd.ID)
	assert.Equal(t, db.CashWithdrawConfirmed, wd.State)
	assert.Equal(t, 1, wd.Depth)

	w.chain.Mine(5)
	w.pass(t)
	wd = w.payout(t, wd.ID)
	assert.Equal(t, db.CashWithdrawFinal, wd.State)
	assert.Zero(t, wd.HeldMillis)
	assert.Equal(t, 1, w.paidCount(t, wd))
}

// U-24.1: the about-to-sign record is committed before FinalizePsbt is called;
// a crash after it is resumed by a new engine that signs the stored PSBT,
// stores and broadcasts it; the chain never confirms two versions.
func TestU_24_1_AboutToSignBeforeSignature(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	checked := 0
	w.chain.OnFinalize = func() {
		var n int64
		require.NoError(t, w.svc.DB.Model(&db.CashWithdrawVersion{}).Where("state = ?", db.CashVersionAboutToSign).Count(&n).Error)
		assert.Equal(t, int64(1), n, "the about-to-sign record exists before any signature")
		checked++
	}
	w.hooks.Before = func(s string) error {
		if s == EngineAfterAboutToSign {
			return ErrEngineStopped
		}
		return nil
	}
	w.passAfterWindow(t)
	assert.Zero(t, checked, "the crash came before the signature")
	assert.True(t, w.payout(t, wd.ID).Signed, "signed is set at the record")
	assert.Nil(t, w.payout(t, wd.ID).Txid)

	w.hooks.Before = nil
	e := w.newEngine()
	require.NoError(t, e.Reconcile(t.Context()))
	assert.Equal(t, 1, checked)
	wd = w.payout(t, wd.ID)
	require.NotNil(t, wd.Txid)
	assert.True(t, w.chain.InMempool(*wd.Txid))
	w.chain.Mine(6)
	w.pass(t)
	assert.Equal(t, db.CashWithdrawFinal, w.payout(t, wd.ID).State)
	assert.Equal(t, 1, w.paidCount(t, wd))
	assert.Len(t, w.versions(t), 1)
}

// U-24.2: a crash between store and publish: the stored raw is published
// unchanged.
func TestU_24_2_CrashBetweenStoreAndPublish(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.hooks.Before = func(s string) error {
		if s == EngineAfterStore {
			return ErrEngineStopped
		}
		return nil
	}
	w.passAfterWindow(t)
	vs := w.versions(t)
	require.Len(t, vs, 1)
	require.Equal(t, db.CashVersionSigned, vs[0].State)
	assert.False(t, w.chain.InMempool(*vs[0].Txid))

	w.hooks.Before = nil
	require.NoError(t, w.newEngine().Reconcile(t.Context()))
	assert.True(t, w.chain.InMempool(*vs[0].Txid), "the stored version, unchanged")
	assert.Equal(t, 1, w.chain.FinalizeCalls)
	assert.Equal(t, *vs[0].Txid, *w.payout(t, wd.ID).Txid)
}

// U-24.8: payouts within the window are batched into one version with a change
// output; a later payout joins by replacement; batch_id never changes.
func TestU_24_8_Batching(t *testing.T) {
	w := newEngineWorld(t, 10)
	a := w.commitTo(t, 21_000_000)
	b := w.commitTo(t, 31_000_000)
	c := w.commitTo(t, 41_000_000)
	w.pass(t)
	assert.Empty(t, w.versions(t), "the window is still open")
	w.passAfterWindow(t)
	vs := w.versions(t)
	require.Len(t, vs, 1)
	outs := outputsOf(&vs[0])
	require.Len(t, outs, 4)
	assert.Nil(t, outs[3].WithdrawalID, "a change output")
	tx := w.chain.Tx(*vs[0].Txid)
	require.NotNil(t, tx)
	assert.Equal(t, int32(2), tx.Version)
	for _, in := range tx.TxIn {
		assert.Equal(t, uint32(0xfffffffd), in.Sequence)
	}
	batch := *w.payout(t, a.ID).BatchID

	d := w.commitTo(t, 51_000_000)
	w.passAfterWindow(t)
	vs = w.versions(t)
	require.Len(t, vs, 2)
	assert.Equal(t, batch, vs[1].BatchID, "joined by replacement")
	assert.Equal(t, db.CashVersionReplaced, vs[0].State)
	assert.Subset(t, inputsOf(&vs[1]), inputsOf(&vs[0]), "input sets only grow")
	for _, wd := range []*db.CashWithdrawal{a, b, c, d} {
		got := w.payout(t, wd.ID)
		assert.Equal(t, batch, *got.BatchID)
		assert.Equal(t, *vs[1].Txid, *got.Txid)
	}
	w.chain.Mine(6)
	w.pass(t)
	for _, wd := range []*db.CashWithdrawal{a, b, c, d} {
		got := w.payout(t, wd.ID)
		assert.Equal(t, db.CashWithdrawFinal, got.State)
		assert.Equal(t, 1, w.paidCount(t, got))
	}
}

// U-24.7: a version unconfirmed six blocks after its first broadcast is
// replaced at a higher rate and absolute fee, same payout output, inputs a
// superset.
func TestU_24_7_Bump(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	first := w.versions(t)[0]
	w.chain.Withhold(*first.Txid, true)
	w.chain.Mine(5)
	w.pass(t)
	assert.Len(t, w.versions(t), 1, "not yet")
	w.chain.Mine(1)
	w.pass(t)
	vs := w.versions(t)
	require.Len(t, vs, 2)
	second := vs[1]
	assert.Greater(t, *second.FeeRateSatKvb, *first.FeeRateSatKvb)
	assert.GreaterOrEqual(t, *second.FeeSat, *first.FeeSat+int64(*second.Vsize))
	assert.Subset(t, inputsOf(&second), inputsOf(&first))
	assert.Equal(t, outputsOf(&first)[0], outputsOf(&second)[0])
	assert.True(t, w.chain.InMempool(*second.Txid))
	assert.False(t, w.chain.InMempool(*first.Txid), "replaced in the mempool")
	w.chain.Withhold(*first.Txid, false)
	w.chain.Mine(6)
	w.pass(t)
	assert.Equal(t, db.CashWithdrawFinal, w.payout(t, wd.ID).State)
	assert.Equal(t, 1, w.paidCount(t, wd))
}
