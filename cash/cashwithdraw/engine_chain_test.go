package cashwithdraw

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/go-flokicoin/chainutil/psbt"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests/fakechain"
)

// U-24.5 / U-24.10: a reorganisation takes a confirmed payout back to broadcast,
// the identical transaction is republished (never replaced by a bump), and it
// confirms again; a version that confirmed once is bumped only through a
// child; depth is capped at final_depth and final is sticky.
func TestU_24_5_Reorg(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	txid := *w.payout(t, wd.ID).Txid
	w.chain.Mine(1)
	w.pass(t)
	require.Equal(t, db.CashWithdrawConfirmed, w.payout(t, wd.ID).State)

	w.chain.Reorg(1, txid) // the network forgot it
	assert.False(t, w.chain.InMempool(txid))
	w.pass(t)
	got := w.payout(t, wd.ID)
	assert.Equal(t, db.CashWithdrawBroadcast, got.State)
	assert.Equal(t, 0, got.Depth)
	assert.True(t, w.chain.InMempool(txid), "the identical transaction is republished")

	// Withheld long past the bump target: still the same parent, bumped by a child.
	w.chain.Withhold(txid, true)
	w.chain.Mine(7)
	w.pass(t)
	vs := w.versions(t)
	assert.Equal(t, txid, *vs[0].Txid)
	var children int
	for _, v := range vs {
		if v.Kind == db.CashVersionChild {
			children++
			assert.Equal(t, vs[0].ID, *v.ParentID)
		} else {
			assert.Equal(t, txid, *v.Txid, "never replaced")
		}
	}
	assert.Equal(t, 1, children)

	w.chain.Withhold(txid, false)
	w.chain.Mine(8)
	w.pass(t)
	got = w.payout(t, wd.ID)
	assert.Equal(t, db.CashWithdrawFinal, got.State)
	assert.Equal(t, 6, got.Depth, "capped at final_depth")
	assert.Equal(t, txid, *got.Txid)
	assert.Equal(t, 1, w.paidCount(t, got))
	assert.Equal(t, int64(1), w.eng.Reorgs.Load())
}

// U-24.5: another version of the batch confirming in the new chain takes over
// txid and vout.
func TestU_24_5_OtherVersionTakesOver(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	first := *w.payout(t, wd.ID).Txid
	w.chain.Withhold(first, true)
	w.chain.Mine(6)
	w.pass(t) // bumped: a second version replaces the first in the mempool
	vs := w.versions(t)
	require.Len(t, vs, 2)
	second := *vs[1].Txid
	w.chain.Mine(1)
	w.pass(t)
	assert.Equal(t, second, *w.payout(t, wd.ID).Txid)
	assert.Equal(t, db.CashWithdrawConfirmed, w.payout(t, wd.ID).State)
	vs = w.versions(t)
	assert.Equal(t, db.CashVersionConflicted, vs[0].State)
}

// U-24.6: a broadcast version is republished every CASH_WITHDRAW_REBROADCAST_SECS
// (an evicted one comes back); a fee refusal bumps at once; the rate is at least
// min relay + 1.
func TestU_24_6_RebroadcastAndFeeRefusal(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.chain.MinRelay = 1000
	w.chain.Estimate = 10
	w.advance(61 * time.Second) // the fee cache
	w.passAfterWindow(t)
	v := w.versions(t)[0]
	assert.GreaterOrEqual(t, *v.FeeRateSatKvb, int64(5000), "min relay 4 sat/vB + 1")
	txid := *v.Txid
	w.chain.Evict(txid)
	w.pass(t)
	assert.False(t, w.chain.InMempool(txid), "not before the rebroadcast period")
	w.advance(time.Duration(w.s.env().RebroadcastSecs()) * time.Second)
	w.pass(t)
	assert.True(t, w.chain.InMempool(txid))

	// The network now refuses the version for its fee: bump at once.
	refuse := true
	w.chain.PublishHook = func(id string) error {
		if id == txid && refuse {
			return lnclient.ErrPublishFeeTooLow
		}
		return nil
	}
	w.chain.Evict(txid)
	w.advance(time.Duration(w.s.env().RebroadcastSecs()) * time.Second)
	w.pass(t)
	vs := w.versions(t)
	require.Len(t, vs, 2)
	assert.True(t, w.chain.InMempool(*vs[1].Txid))
	assert.Equal(t, *vs[1].Txid, *w.payout(t, wd.ID).Txid)
}

// U-24.11 / K-WCAN-1..3: a payout never signed is cancelled and re-minted from
// sent_by + 86400, not a second before.
func TestU_24_11_CancelNow(t *testing.T) {
	w := newEngineWorld(t, 5)
	w.hooks.HoldSign.Store(true)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	assert.False(t, w.payout(t, wd.ID).Signed)
	w.mu.Lock()
	w.now = time.Unix(wd.SentBy+86399, 0)
	w.mu.Unlock()
	w.pass(t)
	assert.Equal(t, db.CashWithdrawQueued, w.payout(t, wd.ID).State)
	w.advance(time.Second)
	w.pass(t)
	got := w.payout(t, wd.ID)
	assert.Equal(t, db.CashWithdrawCancelled, got.State)
	assert.Equal(t, db.CashWithdrawCancelNoRecord, *got.CancelReason)
	assert.Equal(t, int64(21_000_000), balance(w.world, *got.RemintAppID))
	var asides int64
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawSetAside{}).Count(&asides).Error)
	assert.Zero(t, asides, "the set-aside is released")
	var held int64
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawCoin{}).Where("withdrawal_id = ?", wd.ID).Count(&held).Error)
	assert.Zero(t, held)
}

// U-24.11 / U-24.12: a payout with an about-to-sign record but never broadcast
// is invalidated from sent_by + 86400: the invalidating transaction spends from
// every possibly signed version, pays nothing of the cancelled payout, keeps
// the batch's other payouts; cancelled only at final_depth.
func TestU_24_12_InvalidateThenCancel(t *testing.T) {
	w := newEngineWorld(t, 10)
	a := w.commitTo(t, 21_000_000)
	b := w.commitTo(t, 31_000_000)
	w.hooks.HoldBroadcast.Store(true)
	w.passAfterWindow(t)
	vs := w.versions(t)
	require.Len(t, vs, 1)
	payoutVersion := vs[0]

	// Only a is cancelled: b is kept in the invalidating version. Move b's
	// sent_by out of reach.
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawal{}).Where("id = ?", b.ID).Update("sent_by", a.SentBy+10*86400).Error)
	w.mu.Lock()
	w.now = time.Unix(a.SentBy+86400, 0)
	w.mu.Unlock()
	w.hooks.HoldBroadcast.Store(false)
	w.pass(t)
	vs = w.versions(t)
	require.Len(t, vs, 2)
	inv := vs[1]
	assert.Equal(t, db.CashVersionInvalidation, inv.Kind)
	shared := false
	for _, in := range inputsOf(&inv) {
		for _, p := range inputsOf(&payoutVersion) {
			shared = shared || in == p
		}
	}
	assert.True(t, shared, "spends from the about-to-sign version")
	for _, o := range outputsOf(&inv) {
		if o.WithdrawalID != nil {
			assert.Equal(t, b.ID, *o.WithdrawalID, "only b is paid")
		}
	}
	assert.False(t, w.chain.InMempool(*payoutVersion.Txid), "the payout version is no longer broadcast")
	assert.True(t, w.chain.InMempool(*inv.Txid))
	assert.Equal(t, db.CashWithdrawQueued, w.payout(t, a.ID).State)

	w.chain.Mine(5)
	w.pass(t)
	assert.NotEqual(t, db.CashWithdrawCancelled, w.payout(t, a.ID).State, "not before final_depth")
	w.chain.Mine(1)
	w.pass(t)
	gotA := w.payout(t, a.ID)
	assert.Equal(t, db.CashWithdrawCancelled, gotA.State)
	assert.Equal(t, 0, w.paidCount(t, gotA))
	gotB := w.payout(t, b.ID)
	assert.Equal(t, db.CashWithdrawFinal, gotB.State)
	assert.Equal(t, 1, w.paidCount(t, gotB))
	assert.Equal(t, *inv.Txid, *gotB.Txid)
}

// U-24.11: a version paying the payout confirms instead of the invalidation:
// paid, the cancel dropped, no re-mint.
func TestU_24_11_VersionConfirmsInstead(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	txid := *w.payout(t, wd.ID).Txid
	w.chain.Withhold(txid, true)
	w.hooks.HoldBump.Store(true)
	w.mu.Lock()
	w.now = time.Unix(wd.SentBy+604800, 0)
	w.mu.Unlock()
	_, err := w.s.CancelRequest(w.payout(t, wd.ID))
	require.NoError(t, err)
	// The invalidation is built but the network keeps the payout version.
	w.chain.PublishHook = func(id string) error {
		if id != txid {
			return lnclient.ErrPublishFeeTooLow
		}
		return nil
	}
	w.pass(t)
	w.chain.Withhold(txid, false)
	w.chain.Mine(6)
	w.pass(t)
	got := w.payout(t, wd.ID)
	assert.Equal(t, db.CashWithdrawFinal, got.State)
	assert.Nil(t, got.CancelReason, "the cancel is dropped")
	assert.Nil(t, got.RemintAppID)
	assert.Equal(t, 1, w.paidCount(t, got))
}

// U-24.R1: a payout's only version confirms, a reorg takes it out; at sent_by +
// 7 days the holder's cancel request invalidates it, conflicting with that
// once-confirmed version; mined to final_depth, the payout is cancelled and
// re-minted once.
func TestU_24_R1_InvalidateAOnceConfirmedVersion(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	txid := *w.payout(t, wd.ID).Txid
	w.chain.Mine(1)
	w.pass(t)
	w.chain.Reorg(1, txid)
	w.chain.Withhold(txid, true)
	w.pass(t)
	require.Equal(t, db.CashWithdrawBroadcast, w.payout(t, wd.ID).State)
	w.chain.Evict(txid)

	w.mu.Lock()
	w.now = time.Unix(wd.SentBy+604800, 0)
	w.mu.Unlock()
	got, err := w.s.CancelRequest(w.payout(t, wd.ID))
	require.NoError(t, err)
	require.NotNil(t, got.CancelRequestedAt)
	w.hooks.HoldBump.Store(true)
	w.pass(t)
	vs := w.versions(t)
	var inv *db.CashWithdrawVersion
	for i := range vs {
		if vs[i].Kind == db.CashVersionInvalidation {
			inv = &vs[i]
		}
	}
	require.NotNil(t, inv)
	require.True(t, w.chain.InMempool(*inv.Txid))
	w.chain.Mine(6)
	w.pass(t)
	got = w.payout(t, wd.ID)
	assert.Equal(t, db.CashWithdrawCancelled, got.State)
	assert.Equal(t, 0, w.paidCount(t, got))
	_, err = w.s.Remint(context.TODO(), wd.ID, "")
	assert.ErrorIs(t, err, ErrAlreadyReminted)
}

// U-24.13: one notice at the first broadcast and one at the re-mint, each due
// in [600, 1200) s after its commit, on the arrival relay; none at confirmed or
// final.
func TestU_24_13_Notices(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	got := w.payout(t, wd.ID)
	require.NotNil(t, got.NoticeDueAt)
	delay := *got.NoticeDueAt - w.clock().Unix()
	assert.True(t, delay >= 600 && delay < 1200, "delay %d", delay)
	w.pass(t)
	assert.Zero(t, w.sender.count(), "not before its delay")
	w.advance(time.Duration(delay) * time.Second)
	w.pass(t)
	require.Equal(t, 1, w.sender.count())
	assert.Equal(t, "wss://relay.test", w.sender.sent[0].relay)
	assert.Equal(t, [][]string{{"p", wd.Notify}}, [][]string(w.sender.sent[0].ev.Tags))
	assert.Empty(t, w.sender.sent[0].ev.Content)
	w.chain.Mine(6)
	w.pass(t)
	w.advance(2000 * time.Second)
	w.pass(t)
	assert.Equal(t, 1, w.sender.count(), "none at confirmed or final")
}

// U-24.14: an archive fact at each state change, with the txid, vout and miner
// fee share once broadcast.
func TestU_24_14_ArchiveFacts(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	w.chain.Mine(1)
	w.pass(t)
	w.chain.Mine(5)
	w.pass(t)
	var evs []db.CashWithdrawEvent
	require.NoError(t, w.svc.DB.Where("withdrawal_id = ?", wd.ID).Order("id").Find(&evs).Error)
	var states []string
	for _, e := range evs {
		states = append(states, e.State)
	}
	assert.Equal(t, []string{"queued", "broadcast", "confirmed", "final"}, states)
	require.NotNil(t, evs[1].Txid)
	require.NotNil(t, evs[1].MinerFeeMillis)
	assert.Positive(t, *evs[1].MinerFeeMillis)
}

// U-24.16: the unconfirmed value counts queued and broadcast payouts; the
// bound is 10 % of the confirmed balance.
func TestU_24_16_UnconfirmedBound(t *testing.T) {
	w := newEngineWorld(t, 1)
	snap, err := w.eng.Snapshot(context.TODO())
	require.NoError(t, err)
	assert.Equal(t, int64(coinSat), snap.ConfirmedSat)
	at, err := w.eng.AtUnconfirmedBoundTx(w.svc.DB, snap, 10_000_000_000)
	require.NoError(t, err)
	assert.False(t, at, "exactly 10 % is within the bound")
	at, err = w.eng.AtUnconfirmedBoundTx(w.svc.DB, snap, 10_000_000_001)
	require.NoError(t, err)
	assert.True(t, at)
	wd := w.commitTo(t, 21_000_000)
	v, err := UnconfirmedValueTx(w.svc.DB)
	require.NoError(t, err)
	assert.EqualValues(t, wd.PayoutMillis, v)
}

// childFundFails is the chain whose node cannot fund a one-input child: the
// parent's change output is not an unspent wallet coin (the parent left the
// node's mempool).
type childFundFails struct{ *fakechain.Chain }

func (c childFundFails) FundPsbtFixed(ctx context.Context, raw []byte, id [32]byte, lockSecs int64) ([]byte, error) {
	p, err := psbt.NewFromRawBytes(bytes.NewReader(raw), false)
	if err == nil && len(p.UnsignedTx.TxIn) == 1 && len(p.UnsignedTx.TxOut) == 1 {
		return nil, fmt.Errorf("input %s is not an unspent wallet coin", p.UnsignedTx.TxIn[0].PreviousOutPoint)
	}
	return c.Chain.FundPsbtFixed(ctx, raw, id, lockSecs)
}

// A batch whose bump fails (a child its node cannot fund) is retried next
// pass; it does not stop the pass, so another batch's payout is still built,
// broadcast and paid.
func TestEngine_FailedBumpDoesNotStopOtherPayouts(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	txid := *w.payout(t, wd.ID).Txid
	w.chain.Mine(1)
	w.pass(t)
	w.chain.Reorg(1, txid)
	w.pass(t)
	require.Equal(t, db.CashWithdrawBroadcast, w.payout(t, wd.ID).State)

	w.node.payer = childFundFails{w.chain}
	w.chain.Withhold(txid, true)
	w.chain.Mine(7)
	require.NoError(t, w.eng.Pass(context.TODO()), "the failed child is the batch's own")

	other := w.commitTo(t, 31_000_000)
	w.passAfterWindow(t)
	got := w.payout(t, other.ID)
	require.NotNil(t, got.Txid, "the other payout is built and broadcast")
	w.chain.Withhold(txid, false)
	w.chain.Mine(7)
	w.pass(t)
	assert.Equal(t, db.CashWithdrawFinal, w.payout(t, other.ID).State)
	assert.Equal(t, db.CashWithdrawFinal, w.payout(t, wd.ID).State)
}

// fundFails is the chain whose node funds no PSBT while on is set: what lnd
// answers for a replacement whose input its own broadcast version already
// spends ("is not an unspent wallet coin").
type fundFails struct {
	*fakechain.Chain
	on *bool
}

func (c fundFails) FundPsbtFixed(ctx context.Context, raw []byte, id [32]byte, lockSecs int64) ([]byte, error) {
	if *c.on {
		return nil, fmt.Errorf("input is not an unspent wallet coin")
	}
	return c.Chain.FundPsbtFixed(ctx, raw, id, lockSecs)
}

// An invalidation that cannot be built now is retried next pass: the pass
// does not fail on it, and once it can be built the cancel goes through.
func TestEngine_FailedInvalidationDoesNotFailThePass(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	txid := *w.payout(t, wd.ID).Txid
	w.chain.Withhold(txid, true)
	w.hooks.HoldBump.Store(true)
	w.mu.Lock()
	w.now = time.Unix(wd.SentBy+604800, 0)
	w.mu.Unlock()
	_, err := w.s.CancelRequest(w.payout(t, wd.ID))
	require.NoError(t, err)

	fail := true
	w.node.payer = fundFails{w.chain, &fail}
	require.NoError(t, w.eng.Pass(context.TODO()), "the invalidation failing is retried, not the pass's failure")
	require.Len(t, w.versions(t), 1, "nothing built yet")

	fail = false
	w.pass(t)
	require.Len(t, w.versions(t), 2, "the invalidation, built on a later pass")
}

// noPackageRelay is a node without package relay (lokid): every package is
// refused, single transactions relay as usual.
type noPackageRelay struct{ *fakechain.Chain }

func (noPackageRelay) SubmitPackage(context.Context, []byte, []byte) error {
	return fmt.Errorf("-32601: method not found: submitpackage")
}

// A node that refuses the child package gets the child on its own, after its
// parent (plain CPFP): the child is broadcast and the payout confirms.
func TestEngine_ChildWithoutPackageRelay(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	txid := *w.payout(t, wd.ID).Txid
	w.chain.Mine(1)
	w.pass(t)
	w.chain.Reorg(1, txid)
	w.pass(t)
	require.Equal(t, db.CashWithdrawBroadcast, w.payout(t, wd.ID).State)

	w.node.payer = noPackageRelay{w.chain}
	w.chain.Withhold(txid, true)
	w.chain.Mine(7)
	w.pass(t)
	var child *db.CashWithdrawVersion
	for _, v := range w.versions(t) {
		if v.Kind == db.CashVersionChild {
			child = &v
		}
	}
	require.NotNil(t, child, "a child is built")
	assert.Equal(t, db.CashVersionBroadcast, child.State, "published on its own")
	assert.True(t, w.chain.InMempool(*child.Txid))

	w.chain.Withhold(txid, false)
	w.chain.Mine(7)
	w.pass(t)
	got := w.payout(t, wd.ID)
	assert.Equal(t, db.CashWithdrawFinal, got.State)
	assert.Equal(t, txid, *got.Txid)
}
