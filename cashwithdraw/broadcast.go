package cashwithdraw

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/flokiorg/go-flokicoin/wire"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
)

// openBatches are the batches not settled or abandoned.
func (e *Engine) openBatches() ([]db.CashWithdrawBatch, error) {
	var bs []db.CashWithdrawBatch
	err := e.S.DB.Where("state IN ?", []string{db.CashBatchOpen, db.CashBatchSigned}).Order("id").Find(&bs).Error
	return bs, err
}

// current is a batch's version to keep relayed: the newest payout or
// invalidation version that is signed or broadcast, unless one confirmed.
func current(vs []db.CashWithdrawVersion) *db.CashWithdrawVersion {
	for i := len(vs) - 1; i >= 0; i-- {
		v := &vs[i]
		if v.State == db.CashVersionConfirmed || v.State == db.CashVersionFinal {
			return nil
		}
	}
	for i := len(vs) - 1; i >= 0; i-- {
		v := &vs[i]
		if v.Kind == db.CashVersionChild || v.RawTx == nil {
			continue
		}
		if v.State == db.CashVersionSigned || v.State == db.CashVersionBroadcast {
			return v
		}
	}
	return nil
}

// broadcastStep is step 6: each batch's current version is published when
// signed and again every CASH_WITHDRAW_REBROADCAST_SECS (the neutrino backend
// has no mempool view); a version that confirmed once and was reorganised out
// is republished, identical, every pass. A first broadcast makes its payouts
// broadcast and schedules their notice. A fee refusal marks the version for a
// bump now; an input gone is an ERROR, handled by the coin and build steps.
func (e *Engine) broadcastStep(ctx context.Context, p lnclient.OnchainPayer, tip int64) error {
	if e.held(func(h *EngineHooks) bool { return h.HoldBroadcast.Load() }) {
		return nil
	}
	bs, err := e.openBatches()
	if err != nil {
		return err
	}
	now := e.S.now().Unix()
	for _, b := range bs {
		vs, err := e.batchVersions(e.S.DB, b.ID)
		if err != nil {
			return err
		}
		var publish []*db.CashWithdrawVersion
		for i := range vs {
			v := &vs[i]
			if v.ConfirmedOnce && v.State == db.CashVersionBroadcast && v.RawTx != nil {
				publish = append(publish, v) // identical rebroadcast after a reorganisation
			}
		}
		cancelling, err := e.batchCancelling(b.ID)
		if err != nil {
			return err
		}
		if cur := current(vs); cur != nil && !cur.ConfirmedOnce && !(cancelling && cur.Kind == db.CashVersionPayout) {
			due := cur.State == db.CashVersionSigned ||
				(cur.LastBroadcastAt != nil && now-*cur.LastBroadcastAt >= e.env().RebroadcastSecs())
			if due {
				publish = append(publish, cur)
			}
		}
		for _, v := range publish {
			if err := e.publish(ctx, p, v, vs, tip); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) publish(ctx context.Context, p lnclient.OnchainPayer, v *db.CashWithdrawVersion, batch []db.CashWithdrawVersion, tip int64) error {
	raw, err := hex.DecodeString(*v.RawTx)
	if err != nil {
		return err
	}
	err = p.PublishTx(ctx, raw)
	switch {
	case errors.Is(err, lnclient.ErrPublishFeeTooLow):
		logger.Logger.Warn().Uint("version_id", v.ID).Msg("A payout version was refused for its fee; bumping")
		return e.S.DB.Model(&db.CashWithdrawVersion{}).Where("id = ?", v.ID).Update("fee_refused", true).Error
	case errors.Is(err, lnclient.ErrPublishInputGone):
		logger.Logger.Error().Uint("version_id", v.ID).Msg("A payout version's input is missing or spent")
		return e.inputGone(ctx, p, v, tip)
	case err != nil:
		logger.Logger.Warn().Err(err).Uint("version_id", v.ID).Msg("Publishing a payout version failed; retried next pass")
		return nil
	}
	if err := e.markBroadcast(v, batch, tip); err != nil {
		return err
	}
	return e.before(EngineAfterPublish)
}

// markBroadcast records an accepted publish: the version broadcast, the
// versions it replaces replaced, and each payout it pays broadcast at its
// first broadcast (its notice scheduled), else following it.
func (e *Engine) markBroadcast(v *db.CashWithdrawVersion, batch []db.CashWithdrawVersion, tip int64) error {
	now := e.S.now().Unix()
	return e.S.DB.Transaction(func(tx *gorm.DB) error {
		ws, err := e.batchPayouts(tx, v.BatchID)
		if err != nil {
			return err
		}
		if err := lockTx(tx, hubIDsOf(ws)...); err != nil {
			return err
		}
		updates := map[string]interface{}{"state": db.CashVersionBroadcast, "last_broadcast_at": now, "fee_refused": false}
		if v.FirstBroadcastAt == nil {
			updates["first_broadcast_at"], updates["broadcast_height"] = now, tip
		}
		if err := tx.Model(&db.CashWithdrawVersion{}).Where("id = ?", v.ID).Updates(updates).Error; err != nil {
			return err
		}
		if !v.ConfirmedOnce {
			for _, o := range batch {
				if o.ID != v.ID && o.Seq < v.Seq && !o.ConfirmedOnce && o.Kind != db.CashVersionChild &&
					(o.State == db.CashVersionSigned || o.State == db.CashVersionBroadcast) {
					if err := tx.Model(&db.CashWithdrawVersion{}).Where("id = ?", o.ID).Update("state", db.CashVersionReplaced).Error; err != nil {
						return err
					}
				}
			}
		}
		paid := map[uint]int{}
		for i, o := range outputsOf(v) {
			if o.WithdrawalID != nil {
				paid[*o.WithdrawalID] = i
			}
		}
		for i := range ws {
			w := &ws[i]
			vout, ok := paid[w.ID]
			if !ok || w.State == db.CashWithdrawCancelled || w.State == db.CashWithdrawFinal || w.State == db.CashWithdrawConfirmed {
				continue
			}
			first := w.Txid == nil
			upd := map[string]interface{}{"txid": *v.Txid, "vout": vout}
			if first {
				due, err := noticeDue(e.S.now())
				if err != nil {
					return err
				}
				upd["state"], upd["state_at"], upd["notice_due_at"] = db.CashWithdrawBroadcast, now, due
			}
			if err := tx.Model(&db.CashWithdrawal{}).Where("id = ?", w.ID).Updates(upd).Error; err != nil {
				return err
			}
			if first {
				txid := *v.Txid
				w.State, w.Txid, w.Vout = db.CashWithdrawBroadcast, &txid, &vout
				if err := appendEventTx(tx, w, now, minerFeeShare(v)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// bumpStep is step 7: a batch's current version unconfirmed
// CASH_WITHDRAW_BUMP_AFTER_BLOCKS after its first broadcast, refused for its
// fee, or below half the current estimate is replaced by a new version of the
// batch (same payouts, more fee); one that confirmed once is never replaced, and
// is bumped only through a child spending its change.
func (e *Engine) bumpStep(ctx context.Context, p lnclient.OnchainPayer, tip int64, rates Rates) error {
	if e.held(func(h *EngineHooks) bool { return h.HoldBump.Load() }) {
		return nil
	}
	bs, err := e.openBatches()
	if err != nil {
		return err
	}
	for _, b := range bs {
		vs, err := e.batchVersions(e.S.DB, b.ID)
		if err != nil {
			return err
		}
		for i := range vs {
			v := &vs[i]
			if v.ConfirmedOnce && v.State == db.CashVersionBroadcast && v.ConfirmHeight != nil &&
				(v.FeeRefused || tip-*v.ConfirmHeight >= int64(e.env().BumpAfterBlocks())) && !hasChild(vs, v.ID) {
				if err := e.child(ctx, p, rates, v); err != nil {
					return err
				}
			}
		}
		cur := current(vs)
		if cur == nil || cur.ConfirmedOnce || cur.State != db.CashVersionBroadcast && !cur.FeeRefused {
			continue
		}
		stale := cur.BroadcastHeight != nil && tip-*cur.BroadcastHeight >= int64(e.env().BumpAfterBlocks())
		low := cur.FeeRateSatKvb != nil && *cur.FeeRateSatKvb < rates.SatPerVB*1000/2
		if !cur.FeeRefused && !stale && !low {
			continue
		}
		ws, err := e.batchPayouts(e.S.DB, b.ID)
		if err != nil {
			return err
		}
		spec := versionSpec{batchID: b.ID, kind: cur.Kind, prev: cur, hubIDs: hubIDsOf(ws), excluded: map[uint]bool{}}
		for _, set := range possiblySigned(vs) {
			spec.inputs = append(spec.inputs, set...)
		}
		paid := map[uint]bool{}
		for _, o := range outputsOf(cur) {
			if o.WithdrawalID != nil {
				paid[*o.WithdrawalID] = true
			}
		}
		for _, w := range ws {
			if paid[w.ID] {
				spec.payouts = append(spec.payouts, w)
			} else if cur.Kind == db.CashVersionInvalidation {
				spec.excluded[w.ID] = true
			}
		}
		e.Bumps.Add(1)
		if err := e.buildAndSign(ctx, p, tip, rates, spec); err != nil {
			return err
		}
	}
	return nil
}

func hasChild(vs []db.CashWithdrawVersion, parent uint) bool {
	for _, v := range vs {
		if v.ParentID != nil && *v.ParentID == parent {
			return true
		}
	}
	return false
}

// child bumps a once-confirmed version reorganised out: a child spending its
// change output at the target rate for both, submitted as a package.
func (e *Engine) child(ctx context.Context, p lnclient.OnchainPayer, rates Rates, parent *db.CashWithdrawVersion) error {
	outs := outputsOf(parent)
	changeIdx := -1
	for i, o := range outs {
		if o.WithdrawalID == nil {
			changeIdx = i
		}
	}
	if changeIdx < 0 || parent.Vsize == nil || parent.FeeSat == nil {
		return nil
	}
	changeOutpoint := *parent.Txid + ":" + itoa(changeIdx)
	script, err := p.NextChangeScript(ctx)
	if err != nil {
		return err
	}
	childVB := estimateVsize(1, []vout{{Script: hex.EncodeToString(script)}})
	fee := rates.SatPerVB*(int64(*parent.Vsize)+childVB) - *parent.FeeSat
	fee = max(fee, rates.SatPerVB*childVB)
	value := outs[changeIdx].Value - fee
	if value <= 0 {
		return nil
	}
	// The change output is a coin of the wallet now: record it for the child.
	if err := e.S.DB.Save(&db.CashWithdrawCoin{Outpoint: changeOutpoint, ValueSat: outs[changeIdx].Value,
		Script: outs[changeIdx].Script, State: db.CashCoinInVersion, BatchID: &parent.BatchID, SeenAt: e.S.now().Unix()}).Error; err != nil {
		return err
	}
	op, err := parseOutpoint(changeOutpoint)
	if err != nil {
		return err
	}
	mtx := wire.NewMsgTx(2)
	in := wire.NewTxIn(op, nil, nil)
	in.Sequence = sequenceRBF
	mtx.AddTxIn(in)
	mtx.AddTxOut(wire.NewTxOut(value, script))
	ws, err := e.batchPayouts(e.S.DB, parent.BatchID)
	if err != nil {
		return err
	}
	spec := versionSpec{batchID: parent.BatchID, kind: db.CashVersionChild, hubIDs: hubIDsOf(ws)}
	id := parent.ID
	v, err := e.signVersion(ctx, p, spec, mtx, []string{changeOutpoint}, []vout{{Script: hex.EncodeToString(script), Value: value}}, &id)
	if err != nil {
		return err
	}
	if v.State != db.CashVersionSigned {
		return nil
	}
	parentRaw, _ := hex.DecodeString(*parent.RawTx)
	childRaw, _ := hex.DecodeString(*v.RawTx)
	if err := p.SubmitPackage(ctx, parentRaw, childRaw); err != nil {
		logger.Logger.Warn().Err(err).Msg("Submitting a payout's child package failed; retried next pass")
		return nil
	}
	e.Bumps.Add(1)
	now := e.S.now().Unix()
	return e.S.DB.Model(&db.CashWithdrawVersion{}).Where("id = ?", v.ID).
		Updates(map[string]interface{}{"state": db.CashVersionBroadcast, "first_broadcast_at": now, "last_broadcast_at": now}).Error
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}

// batchCancelling: a payout of the batch is being cancelled, so its payout
// versions are no longer broadcast (an invalidation replaces them).
func (e *Engine) batchCancelling(batchID uint) (bool, error) {
	var n int64
	err := e.S.DB.Model(&db.CashWithdrawal{}).
		Where("batch_id = ? AND cancel_reason IS NOT NULL AND state <> ?", batchID, db.CashWithdrawCancelled).Count(&n).Error
	return n > 0, err
}

// inputGone handles a version refused for a missing or spent input: the inputs
// the wallet no longer holds (spent by someone else's transaction, since no
// version of ours confirmed) are vanished, and the batch is rebuilt from its
// remaining inputs and new coins. The version stays possibly signed, so the
// next one must still spend from it, until the outside spend is at
// final_depth (invalidateGone).
func (e *Engine) inputGone(ctx context.Context, p lnclient.OnchainPayer, v *db.CashWithdrawVersion, tip int64) error {
	utxos, err := p.ListUnspent(ctx, 0, "")
	if err != nil {
		return err
	}
	unspent := map[string]bool{}
	for _, u := range utxos {
		unspent[u.Outpoint] = true
	}
	now := e.S.now().Unix()
	for _, in := range inputsOf(v) {
		if unspent[in] {
			continue
		}
		res := e.S.DB.Model(&db.CashWithdrawCoin{}).Where("outpoint = ? AND state <> ?", in, db.CashCoinVanished).
			Updates(map[string]interface{}{"state": db.CashCoinVanished, "vanished_at": now, "vanished_height": tip})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			e.Vanished.Add(1)
			logger.Logger.Error().Str("outpoint", in).Msg("An input of a payout version was spent by another transaction")
		}
	}
	return e.S.DB.Model(&db.CashWithdrawVersion{}).Where("id = ?", v.ID).Update("fee_refused", true).Error
}

// invalidateGone marks invalid, and leaves out of every later version, a
// version holding an input spent by someone else's transaction once that spend
// is at final_depth (counted, conservatively, from when the engine saw it gone).
func (e *Engine) invalidateGone(tip int64) error {
	var coins []db.CashWithdrawCoin
	if err := e.S.DB.Where("state = ? AND vanished_height IS NOT NULL", db.CashCoinVanished).Find(&coins).Error; err != nil {
		return err
	}
	if len(coins) == 0 {
		return nil
	}
	fd, err := e.coinMinConfs()
	if err != nil {
		return err
	}
	gone := map[string]bool{}
	for _, c := range coins {
		if tip-*c.VanishedHeight+1 >= int64(fd) {
			gone[c.Outpoint] = true
		}
	}
	var vs []db.CashWithdrawVersion
	if err := e.S.DB.Where("state IN ?", []string{db.CashVersionSigned, db.CashVersionBroadcast, db.CashVersionReplaced, db.CashVersionAboutToSign}).
		Find(&vs).Error; err != nil {
		return err
	}
	for i := range vs {
		for _, in := range inputsOf(&vs[i]) {
			if gone[in] {
				if err := e.S.DB.Model(&db.CashWithdrawVersion{}).Where("id = ?", vs[i].ID).Update("state", db.CashVersionInvalid).Error; err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}
