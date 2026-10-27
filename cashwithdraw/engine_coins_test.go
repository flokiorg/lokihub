package cashwithdraw

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

func (w *engineWorld) coin(t *testing.T, outpoint string) *db.CashWithdrawCoin {
	t.Helper()
	var c db.CashWithdrawCoin
	require.NoError(t, w.svc.DB.Where("outpoint = ?", outpoint).Limit(1).Find(&c).Error)
	if c.Outpoint == "" {
		return nil
	}
	return &c
}

// U-24.9: only coins at final_depth; order deposits and coins another lease
// holds excluded; the floor of 2 × RequiredReserve never set aside; set-aside
// coins leased under the engine's id and re-leased; the operator's own spends
// refused into set-asides.
func TestU_24_9_Coins(t *testing.T) {
	w := newEngineWorld(t, 1)
	foreign := w.chain.Fund(coinSat, 6)
	w.chain.LeaseForeign(foreign)
	orderCoin := w.chain.Fund(coinSat, 6)
	young := w.chain.Fund(coinSat, 3)
	orderScript := w.chain.Tx(orderCoin[:64]).TxOut[0].PkScript
	w.s.Orders = orderAddress{script: orderScript}
	w.pass(t)
	assert.Nil(t, w.coin(t, young), "below final_depth")
	assert.Nil(t, w.coin(t, foreign), "leased by someone else")
	assert.Nil(t, w.coin(t, orderCoin), "an order deposit")
	w.chain.Mine(3)
	w.pass(t)
	require.NotNil(t, w.coin(t, young))
	assert.Equal(t, db.CashCoinAvailable, w.coin(t, young).State)

	// The floor: 2 × the node's reserve is never set aside.
	assert.Equal(t, 2*w.chain.Reserve, w.eng.floorSat.Load())
	w.chain.Reserve = coinSat // floor = 2 coins of the 2 we hold
	w.pass(t)
	ok, err := w.eng.CanSetAsideTx(w.svc.DB, 1_000_000, 0)
	require.NoError(t, err)
	assert.False(t, ok)
	w.chain.Reserve = 10_000
	w.pass(t)

	w.svc.Cfg.GetEnv().WithdrawEnv.CashWithdrawLeaseSecs = 7*86400 + 3600
	wd := w.commitTo(t, 21_000_000)
	var assigned []db.CashWithdrawCoin
	require.NoError(t, w.svc.DB.Where("withdrawal_id = ?", wd.ID).Find(&assigned).Error)
	require.NotEmpty(t, assigned)
	w.hooks.HoldSign.Store(true)
	w.pass(t)
	id, err := w.eng.lease()
	require.NoError(t, err)
	exp := w.chain.LeaseOf(id, assigned[0].Outpoint)
	assert.Positive(t, exp, "leased at the node")

	// Re-leased when less than 7 days remain.
	w.advance(2 * time.Hour)
	w.pass(t)
	assert.Greater(t, w.chain.LeaseOf(id, assigned[0].Outpoint), exp)

	// The operator's own spend.
	assert.ErrorIs(t, w.eng.SpendCheck(context.TODO(), 1000, true), ErrSpendSetAside, "no send-all while anything is set aside")
	assert.ErrorIs(t, w.eng.SpendCheck(context.TODO(), 4*coinSat-1000, false), ErrSpendSetAside, "into the set-aside")
	assert.NoError(t, w.eng.SpendCheck(context.TODO(), 1000, false))
}

// U-24.9: a set-aside coin that vanishes before the payout's first signature is
// replaced; after it the version holding it is handled by the broadcast step.
func TestU_24_9_VanishedCoinReplaced(t *testing.T) {
	w := newEngineWorld(t, 3)
	wd := w.commitTo(t, 21_000_000)
	var assigned []db.CashWithdrawCoin
	require.NoError(t, w.svc.DB.Where("withdrawal_id = ?", wd.ID).Find(&assigned).Error)
	require.Len(t, assigned, 1)
	w.chain.SpendOutside(assigned[0].Outpoint)
	w.hooks.HoldSign.Store(true)
	w.pass(t)
	assert.Equal(t, db.CashCoinVanished, w.coin(t, assigned[0].Outpoint).State)
	var now []db.CashWithdrawCoin
	require.NoError(t, w.svc.DB.Where("withdrawal_id = ? AND state = ?", wd.ID, db.CashCoinSetAside).Find(&now).Error)
	require.Len(t, now, 1, "replaced from the available coins")
	assert.NotEqual(t, assigned[0].Outpoint, now[0].Outpoint)
	assert.Equal(t, int64(1), w.eng.Vanished.Load())

	w.hooks.HoldSign.Store(false)
	w.passAfterWindow(t)
	w.chain.Mine(6)
	w.pass(t)
	assert.Equal(t, db.CashWithdrawFinal, w.payout(t, wd.ID).State)
}

// U-24.19: an input of a broadcast version spent by someone else: the next
// version must still spend from it (SpendsFromEach includes it) while the
// outside spend is below final_depth; once it is, the version is invalid and
// left out.
func TestU_24_19_OutsideSpendOfAnInput(t *testing.T) {
	w := newEngineWorld(t, 6)
	a := w.commitTo(t, 21_000_000)
	b := w.commitTo(t, 31_000_000)
	w.passAfterWindow(t)
	first := w.versions(t)[0]
	require.Len(t, inputsOf(&first), 2)
	gone := inputsOf(&first)[0]
	w.chain.SpendOutside(gone)
	assert.False(t, w.chain.InMempool(*first.Txid))
	w.advance(time.Duration(w.s.env().RebroadcastSecs()) * time.Second)
	w.pass(t)
	vs := w.versions(t)
	require.Len(t, vs, 2)
	second := vs[1]
	assert.NotContains(t, inputsOf(&second), gone)
	assert.True(t, nipcash.SpendsFromEach([][]string{inputsOf(&first)}, inputsOf(&second)), "still spends from the first")
	assert.NotEqual(t, db.CashVersionInvalid, w.versions(t)[0].State, "possibly signed until the outside spend is final")
	w.chain.Withhold(*second.Txid, true)
	w.hooks.HoldBump.Store(true)
	w.chain.Mine(6)
	w.pass(t)
	assert.Equal(t, db.CashVersionInvalid, w.versions(t)[0].State, "left out once the outside spend is final")
	w.hooks.HoldBump.Store(false)
	w.chain.Withhold(*second.Txid, false)
	w.chain.Mine(6)
	w.pass(t)
	for _, wd := range []*db.CashWithdrawal{a, b} {
		got := w.payout(t, wd.ID)
		assert.Equal(t, db.CashWithdrawFinal, got.State)
		assert.Equal(t, 1, w.paidCount(t, got))
	}
}

// U-24.4: assertVersionInvariants refuses a version that drops or changes a
// signed payout's output, pays another batch's payout, exceeds the cap, lacks
// change, or shares no input with an earlier version.
func TestU_24_4_Invariants(t *testing.T) {
	w := newEngineWorld(t, 6)
	a := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	b := w.commitTo(t, 31_000_000)
	w.hooks.HoldSign.Store(true)
	w.passAfterWindow(t) // b joins the open batch by replacement only once signing resumes
	w.hooks.HoldSign.Store(false)
	v := w.versions(t)[0]
	batch := v.BatchID
	a = w.payout(t, a.ID)
	b = w.payout(t, b.ID)
	outs := outputsOf(&v)
	ins := inputsOf(&v)
	change := outs[len(outs)-1]
	dust := int64(294)
	ok := func(spec versionSpec, in []string, out []vout) error {
		return w.eng.assertVersionInvariants(w.svc.DB, spec, in, out, dust)
	}
	spec := versionSpec{batchID: batch, kind: db.CashVersionPayout}
	require.NoError(t, ok(spec, ins, outs))

	assert.ErrorIs(t, ok(spec, ins, []vout{change}), errInvariant, "drops a signed payout")
	changed := append([]vout(nil), outs...)
	changed[0].Value--
	assert.ErrorIs(t, ok(spec, ins, changed), errInvariant, "changes its value")
	assert.ErrorIs(t, ok(spec, []string{strings64("ff") + ":0"}, outs), errInvariant, "spends from no earlier version")
	noChange := outs[:len(outs)-1]
	assert.ErrorIs(t, ok(spec, ins, noChange), errInvariant)
	small := append([]vout(nil), outs...)
	small[len(small)-1].Value = 1
	assert.ErrorIs(t, ok(spec, ins, small), errInvariant, "change below dust")

	// b in another batch: a new batch's version may not pay a.
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawal{}).Where("id = ?", b.ID).Update("batch_id", batch+100).Error)
	bid := b.ID
	withB := append(append([]vout(nil), outs[:len(outs)-1]...), vout{Script: b.OutputScript, Value: b.PayoutMillis / 1000, WithdrawalID: &bid}, change)
	assert.ErrorIs(t, ok(spec, ins, withB), errInvariant, "pays another batch's payout")

	w.svc.Cfg.GetEnv().WithdrawEnv.CashWithdrawMaxOutputs = 1
	defer func() { w.svc.Cfg.GetEnv().WithdrawEnv.CashWithdrawMaxOutputs = 0 }()
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawal{}).Where("id = ?", b.ID).Update("batch_id", batch).Error)
	assert.ErrorIs(t, ok(spec, ins, withB), errInvariant, "over the output cap")
	assert.Positive(t, w.eng.InvariantFailures.Load())

	// The DB guard: a payout's batch never changes.
	err := w.svc.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&db.CashWithdrawal{}).Where("id = ? AND (batch_id IS NULL OR batch_id = ?)", a.ID, batch+1).Update("batch_id", batch+1)
		assert.Zero(t, res.RowsAffected)
		return res.Error
	})
	require.NoError(t, err)
}

func strings64(s string) string {
	out := ""
	for len(out) < 64 {
		out += s
	}
	return out[:64]
}

var _ = hex.EncodeToString
