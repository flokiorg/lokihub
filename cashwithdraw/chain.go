package cashwithdraw

import (
	"context"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
)

// chainStep is step 2: every version of an open batch that has a transaction
// is read from the wallet. A newly confirmed one becomes the batch's confirmed
// version (the others conflicted) and its payouts follow it, to final at
// final_depth (sticky); one whose block left the best chain is broadcast again
// and its payouts with it, and the identical transaction is republished; an
// invalidation at final_depth cancels and re-mints the payouts it invalidated.
func (e *Engine) chainStep(ctx context.Context, p lnclient.OnchainPayer, tip int64) error {
	var vs []db.CashWithdrawVersion
	if err := e.S.DB.Joins("JOIN cash_withdraw_batches b ON b.id = cash_withdraw_versions.batch_id").
		Where("b.state IN ? AND cash_withdraw_versions.txid IS NOT NULL AND cash_withdraw_versions.state <> ?",
			[]string{db.CashBatchOpen, db.CashBatchSigned}, db.CashVersionInvalid).
		Order("cash_withdraw_versions.id").Find(&vs).Error; err != nil {
		return err
	}
	var remints []remintDue
	var unlease []string
	for i := range vs {
		v := &vs[i]
		wt, err := p.WalletTx(ctx, *v.Txid)
		if err != nil {
			return err
		}
		switch {
		case wt.Confs >= 1:
			due, freed, err := e.onConfirmed(v, wt)
			if err != nil {
				return err
			}
			remints = append(remints, due...)
			unlease = append(unlease, freed...)
		case v.State == db.CashVersionConfirmed || v.State == db.CashVersionFinal:
			if err := e.onReorged(v); err != nil {
				return err
			}
		}
	}
	for _, r := range remints {
		if _, err := e.S.Remint(ctx, r.id, r.reason); err != nil && err != ErrAlreadyReminted {
			logger.Logger.Error().Err(err).Uint("withdrawal_id", r.id).Msg("Re-mint of a cancelled payout failed; retried next pass")
		}
	}
	if len(unlease) > 0 {
		if id, err := e.lease(); err == nil {
			for _, c := range unlease {
				_ = p.ReleaseOutput(ctx, id, c)
			}
		}
	}
	return e.before(EngineAfterChainUpdate)
}

type remintDue struct {
	id     uint
	reason string
}

func (e *Engine) batchPayouts(tx *gorm.DB, batchID uint) ([]db.CashWithdrawal, error) {
	var ws []db.CashWithdrawal
	err := tx.Where("batch_id = ?", batchID).Order("id").Find(&ws).Error
	return ws, err
}

func hubIDsOf(ws []db.CashWithdrawal) []uint {
	out := make([]uint, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.HubAppID)
	}
	return out
}

// minerFeeShare is a payout's share of its version's fee, in millis.
func minerFeeShare(v *db.CashWithdrawVersion) *int64 {
	if v.FeeSat == nil {
		return nil
	}
	n := 0
	for _, o := range outputsOf(v) {
		if o.WithdrawalID != nil {
			n++
		}
	}
	share := *v.FeeSat * 1000 / int64(max(n, 1))
	return &share
}

// onConfirmed follows a confirmed version: the payouts it pays to confirmed and
// final. A payout of the batch it does not pay (one that joined by a later
// version, or one an invalidation excludes) can be paid by no other version
// once it is at the payout's final_depth, since every version of a batch
// spends from every other: then a payout being cancelled is re-minted, and
// any other is released from the batch to be paid in a new one (R2-P-H1).
// The batch settles once every payout has ended or left; only the confirmed
// version's inputs are spent, the coins only other versions used return to
// the pool, and their leases are returned (freed).
func (e *Engine) onConfirmed(v *db.CashWithdrawVersion, wt lnclient.WalletTx) (due []remintDue, freed []string, err error) {
	err = e.S.DB.Transaction(func(tx *gorm.DB) error {
		due, freed = nil, nil
		ws, err := e.batchPayouts(tx, v.BatchID)
		if err != nil {
			return err
		}
		if err := lockTx(tx, hubIDsOf(ws)...); err != nil {
			return err
		}
		now := e.S.now().Unix()
		if (v.State != db.CashVersionConfirmed && v.State != db.CashVersionFinal) || v.ConfirmHash == nil || *v.ConfirmHash != wt.BlockHash {
			if err := tx.Model(&db.CashWithdrawVersion{}).Where("id = ?", v.ID).Updates(map[string]interface{}{
				"state": db.CashVersionConfirmed, "confirmed_once": true, "confirm_height": wt.BlockHeight, "confirm_hash": wt.BlockHash,
			}).Error; err != nil {
				return err
			}
			if err := tx.Model(&db.CashWithdrawVersion{}).
				Where("batch_id = ? AND id <> ? AND kind <> ? AND state IN ?", v.BatchID, v.ID, db.CashVersionChild,
					[]string{db.CashVersionAboutToSign, db.CashVersionSigned, db.CashVersionBroadcast, db.CashVersionReplaced, db.CashVersionConfirmed}).
				Update("state", db.CashVersionConflicted).Error; err != nil {
				return err
			}
		}
		paid := map[uint]int{}
		for i, o := range outputsOf(v) {
			if o.WithdrawalID != nil {
				paid[*o.WithdrawalID] = i
			}
		}
		txid := *v.Txid
		allEnded, maxFD := true, 0
		for i := range ws {
			w := &ws[i]
			maxFD = max(maxFD, w.FinalDepth)
			vout, isPaid := paid[w.ID]
			if !isPaid {
				if w.State == db.CashWithdrawCancelled {
					continue
				}
				if wt.Confs < int64(w.FinalDepth) {
					// A reorganisation could still confirm a version paying it.
					allEnded = false
					continue
				}
				// No version of this payout can confirm any more.
				if w.CancelReason != nil {
					if err := tx.Delete(&db.CashWithdrawSetAside{}, "withdrawal_id = ?", w.ID).Error; err != nil {
						return err
					}
					due = append(due, remintDue{id: w.ID, reason: *w.CancelReason})
					continue
				}
				if err := e.releaseFromBatchTx(tx, w, v); err != nil {
					return err
				}
				continue
			}
			depth := int(min(wt.Confs, int64(w.FinalDepth)))
			state := db.CashWithdrawConfirmed
			updates := map[string]interface{}{"txid": txid, "vout": vout, "depth": depth}
			if w.FinalReached || depth >= w.FinalDepth {
				state = db.CashWithdrawFinal
				if !w.FinalReached {
					updates["final_reached"], updates["final_at"], updates["held_millis"] = true, now, 0
					if err := tx.Delete(&db.CashWithdrawSetAside{}, "withdrawal_id = ?", w.ID).Error; err != nil {
						return err
					}
				}
			} else {
				allEnded = false
			}
			if w.CancelReason != nil {
				logger.Logger.Info().Uint("withdrawal_id", w.ID).Msg("A version paying a payout confirmed: the payout is paid, its cancel dropped")
				updates["cancel_reason"], updates["cancel_requested_at"] = nil, nil
			}
			changed := w.State != state || w.Txid == nil || *w.Txid != txid
			updates["state"] = state
			if changed {
				updates["state_at"] = now
			}
			if err := tx.Model(&db.CashWithdrawal{}).Where("id = ?", w.ID).Updates(updates).Error; err != nil {
				return err
			}
			if changed {
				w.State, w.Txid, w.Vout, w.Depth = state, &txid, &vout, depth
				if err := appendEventTx(tx, w, now, minerFeeShare(v)); err != nil {
					return err
				}
			}
		}
		if allEnded && wt.Confs >= int64(maxFD) && len(due) == 0 {
			if err := tx.Model(&db.CashWithdrawBatch{}).Where("id = ?", v.BatchID).Update("state", db.CashBatchSettled).Error; err != nil {
				return err
			}
			// Only the confirmed version's inputs are spent; a coin only a
			// replaced or conflicted version used is still the wallet's.
			ins := append(inputsOf(v), "")
			if err := tx.Model(&db.CashWithdrawCoin{}).Where("outpoint IN ?", ins).
				Updates(map[string]interface{}{"state": db.CashCoinSpent, "withdrawal_id": nil}).Error; err != nil {
				return err
			}
			if err := tx.Model(&db.CashWithdrawCoin{}).Where("batch_id = ? AND state = ? AND outpoint NOT IN ?", v.BatchID, db.CashCoinInVersion, ins).
				Pluck("outpoint", &freed).Error; err != nil {
				return err
			}
			if err := tx.Model(&db.CashWithdrawCoin{}).Where("outpoint IN ?", append(freed, "")).
				Updates(map[string]interface{}{"state": db.CashCoinAvailable, "withdrawal_id": nil, "batch_id": nil, "leased_until": nil}).Error; err != nil {
				return err
			}
			if err := tx.Model(&db.CashWithdrawVersion{}).Where("id = ?", v.ID).Update("state", db.CashVersionFinal).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return due, freed, err
}

// releaseFromBatchTx takes a payout out of a batch whose confirmed version v,
// at the payout's final_depth, does not pay it: every version that pays it
// conflicts with v, so none can confirm. It keeps its about-to-sign mark and
// its set-aside; its own coins v did not spend are set aside for it again,
// and the build step pays it in a new batch.
func (e *Engine) releaseFromBatchTx(tx *gorm.DB, w *db.CashWithdrawal, v *db.CashWithdrawVersion) error {
	ins := append(inputsOf(v), "")
	if err := tx.Model(&db.CashWithdrawCoin{}).Where("withdrawal_id = ? AND state = ? AND batch_id = ? AND outpoint NOT IN ?",
		w.ID, db.CashCoinInVersion, v.BatchID, ins).
		Updates(map[string]interface{}{"state": db.CashCoinSetAside, "batch_id": nil}).Error; err != nil {
		return err
	}
	if err := tx.Model(&db.CashWithdrawal{}).Where("id = ? AND batch_id = ?", w.ID, v.BatchID).
		Update("batch_id", nil).Error; err != nil {
		return err
	}
	logger.Logger.Warn().Uint("withdrawal_id", w.ID).Uint("batch_id", v.BatchID).
		Msg("An earlier version of a payout's batch confirmed without it; the payout is paid in a new batch")
	return nil
}

// onReorged returns a version whose block left the best chain to broadcast,
// with its confirmed payouts; final stays final (an ERROR: the reorganisation
// went past final_depth) and the version is still republished.
func (e *Engine) onReorged(v *db.CashWithdrawVersion) error {
	e.Reorgs.Add(1)
	return e.S.DB.Transaction(func(tx *gorm.DB) error {
		ws, err := e.batchPayouts(tx, v.BatchID)
		if err != nil {
			return err
		}
		if err := lockTx(tx, hubIDsOf(ws)...); err != nil {
			return err
		}
		if err := tx.Model(&db.CashWithdrawVersion{}).Where("id = ?", v.ID).
			Updates(map[string]interface{}{"state": db.CashVersionBroadcast, "confirm_hash": nil}).Error; err != nil {
			return err
		}
		if err := tx.Model(&db.CashWithdrawBatch{}).Where("id = ?", v.BatchID).Update("state", db.CashBatchOpen).Error; err != nil {
			return err
		}
		now := e.S.now().Unix()
		for i := range ws {
			w := &ws[i]
			if w.Txid == nil || *w.Txid != *v.Txid {
				continue
			}
			if w.FinalReached {
				logger.Logger.Error().Uint("withdrawal_id", w.ID).Msg("A reorganisation went past a final payout's final_depth; its version is republished")
				continue
			}
			if err := tx.Model(&db.CashWithdrawal{}).Where("id = ?", w.ID).
				Updates(map[string]interface{}{"state": db.CashWithdrawBroadcast, "depth": 0, "state_at": now}).Error; err != nil {
				return err
			}
			w.State, w.Depth = db.CashWithdrawBroadcast, 0
			if err := appendEventTx(tx, w, now, nil); err != nil {
				return err
			}
		}
		return nil
	})
}
