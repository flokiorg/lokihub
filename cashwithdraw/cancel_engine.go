package cashwithdraw

import (
	"context"
	"errors"

	"github.com/ohstr/nmilat/nipcash"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
)

// payoutState is a payout's reported state.
func payoutState(w *db.CashWithdrawal) nipcash.PayoutState {
	return nipcash.PayoutStateOf(nipcash.PayoutFacts{Cancelled: w.State == db.CashWithdrawCancelled, Broadcast: w.Txid != nil,
		FinalReached: w.FinalReached, Depth: uint32(max(w.Depth, 0)), FinalDepth: uint32(max(w.FinalDepth, 0))}) //nolint:gosec // small
}

// cancelStep is step 8. A payout never signed is cancelled and re-minted from
// sent_by + 1 day (its set-aside released); one with an about-to-sign record but
// never broadcast is invalidated first, as is a broadcast one whose holder asked
// from sent_by + 7 days. The invalidating transaction is a version of the
// payout's batch that spends from every version that may have been signed, pays
// none of the cancelled payout's outputs and keeps every other payout's; the
// payout is re-minted once it is at final_depth (chain step). Invalidating is
// not bumping: it may conflict with a version that confirmed once.
func (e *Engine) cancelStep(ctx context.Context, p lnclient.OnchainPayer, tip int64, rates Rates) error {
	var ws []db.CashWithdrawal
	if err := e.S.DB.Where("state IN ?", []string{db.CashWithdrawQueued, db.CashWithdrawBroadcast, db.CashWithdrawConfirmed}).
		Order("id").Find(&ws).Error; err != nil {
		return err
	}
	now := e.S.cancelNow().Unix()
	id, err := e.lease()
	if err != nil {
		return err
	}
	for i := range ws {
		w := &ws[i]
		step := nipcash.CancelDue(nipcash.CancelFacts{AboutToSign: w.Signed, Broadcast: w.Txid != nil, State: payoutState(w), SentBy: w.SentBy}, now)
		switch step {
		case nipcash.CancelNow:
			var coins []string
			err := e.S.DB.Transaction(func(tx *gorm.DB) error {
				if err := lockTx(tx, w.HubAppID); err != nil {
					return err
				}
				if err := tx.Model(&db.CashWithdrawCoin{}).Where("state = ? AND withdrawal_id = ?", db.CashCoinSetAside, w.ID).
					Pluck("outpoint", &coins).Error; err != nil {
					return err
				}
				if err := tx.Model(&db.CashWithdrawCoin{}).Where("state = ? AND withdrawal_id = ?", db.CashCoinSetAside, w.ID).
					Updates(map[string]interface{}{"state": db.CashCoinAvailable, "withdrawal_id": nil}).Error; err != nil {
					return err
				}
				if err := tx.Delete(&db.CashWithdrawSetAside{}, "withdrawal_id = ?", w.ID).Error; err != nil {
					return err
				}
				return tx.Model(&db.CashWithdrawal{}).Where("id = ? AND signed = ?", w.ID, false).
					Update("cancel_reason", db.CashWithdrawCancelNoRecord).Error
			})
			if err != nil {
				return err
			}
			for _, c := range coins {
				_ = p.ReleaseOutput(ctx, id, c)
			}
			if _, err := e.S.Remint(ctx, w.ID, db.CashWithdrawCancelNoRecord); err != nil && err != ErrAlreadyReminted {
				logger.Logger.Error().Err(err).Uint("withdrawal_id", w.ID).Msg("Re-mint of a never-signed payout failed; retried next pass")
			}
			continue
		case nipcash.InvalidateThenCancel:
			if w.CancelReason == nil {
				reason := db.CashWithdrawCancelInvalidate
				if err := e.S.DB.Model(&db.CashWithdrawal{}).Where("id = ? AND cancel_reason IS NULL", w.ID).
					Update("cancel_reason", reason).Error; err != nil {
					return err
				}
				w.CancelReason = &reason
			}
		default:
			// CancelNotDue: only an already-recorded cancel reason is acted on below.
		}
		if w.CancelReason != nil && *w.CancelReason != db.CashWithdrawCancelNoRecord && w.BatchID == nil && w.Signed {
			// Released from a batch whose final version does not pay it, and
			// in no new one yet: no version of it can confirm, so it is
			// cancelled at once.
			if err := e.cancelReleased(ctx, p, id, w); err != nil {
				return err
			}
			continue
		}
		if w.CancelReason != nil && *w.CancelReason != db.CashWithdrawCancelNoRecord && w.BatchID != nil {
			// A batch whose invalidation cannot be built now is retried next
			// pass; it does not stop the other payouts' steps.
			if err := e.invalidate(ctx, p, tip, rates, *w.BatchID); err != nil {
				if errors.Is(err, ErrEngineStopped) {
					return err
				}
				logger.Logger.Warn().Err(err).Uint("withdrawal_id", w.ID).Uint("batch_id", *w.BatchID).
					Msg("Invalidating a payout batch failed; retried next pass")
			}
		}
	}
	return nil
}

// cancelReleased cancels and re-mints a payout released from its batch: its
// set-aside and coins are returned first.
func (e *Engine) cancelReleased(ctx context.Context, p lnclient.OnchainPayer, id [32]byte, w *db.CashWithdrawal) error {
	var coins []string
	err := e.S.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockTx(tx, w.HubAppID); err != nil {
			return err
		}
		var cur db.CashWithdrawal
		if err := tx.Where("id = ?", w.ID).Limit(1).Find(&cur).Error; err != nil {
			return err
		}
		if cur.BatchID != nil || cur.State == db.CashWithdrawCancelled {
			return nil
		}
		if err := tx.Model(&db.CashWithdrawCoin{}).Where("state = ? AND withdrawal_id = ?", db.CashCoinSetAside, w.ID).
			Pluck("outpoint", &coins).Error; err != nil {
			return err
		}
		if err := tx.Model(&db.CashWithdrawCoin{}).Where("state = ? AND withdrawal_id = ?", db.CashCoinSetAside, w.ID).
			Updates(map[string]interface{}{"state": db.CashCoinAvailable, "withdrawal_id": nil, "leased_until": nil}).Error; err != nil {
			return err
		}
		return tx.Delete(&db.CashWithdrawSetAside{}, "withdrawal_id = ?", w.ID).Error
	})
	if err != nil {
		return err
	}
	for _, c := range coins {
		_ = p.ReleaseOutput(ctx, id, c)
	}
	if _, err := e.S.Remint(ctx, w.ID, *w.CancelReason); err != nil && err != ErrAlreadyReminted {
		logger.Logger.Error().Err(err).Uint("withdrawal_id", w.ID).Msg("Re-mint of a released payout failed; retried next pass")
	}
	return nil
}

// invalidate builds the batch's invalidating transaction unless a live one
// already excludes every payout being cancelled (then bumping keeps it moving).
// While a version of the batch is confirmed nothing is built: its inputs are
// spent, so no invalidation could confirm; that version, at final_depth,
// invalidates every payout it does not pay (onConfirmed).
func (e *Engine) invalidate(ctx context.Context, p lnclient.OnchainPayer, tip int64, rates Rates, batchID uint) error {
	vs, err := e.batchVersions(e.S.DB, batchID)
	if err != nil {
		return err
	}
	for i := range vs {
		if vs[i].State == db.CashVersionConfirmed || vs[i].State == db.CashVersionFinal {
			return nil
		}
	}
	ws, err := e.batchPayouts(e.S.DB, batchID)
	if err != nil {
		return err
	}
	excluded := map[uint]bool{}
	var keep []db.CashWithdrawal
	for _, w := range ws {
		switch {
		case w.State == db.CashWithdrawCancelled:
		case w.CancelReason != nil:
			excluded[w.ID] = true
		default:
			keep = append(keep, w)
		}
	}
	if len(excluded) == 0 {
		return nil
	}
	for i := len(vs) - 1; i >= 0; i-- {
		v := &vs[i]
		if v.Kind != db.CashVersionInvalidation || v.State == db.CashVersionInvalid || v.State == db.CashVersionConflicted ||
			v.State == db.CashVersionReplaced || v.State == db.CashVersionMismatch {
			continue
		}
		covers := true
		paid := map[uint]bool{}
		for _, o := range outputsOf(v) {
			if o.WithdrawalID != nil {
				paid[*o.WithdrawalID] = true
			}
		}
		for id := range excluded {
			if paid[id] {
				covers = false
			}
		}
		if covers {
			return nil
		}
	}
	spec := versionSpec{batchID: batchID, kind: db.CashVersionInvalidation, payouts: keep, excluded: excluded, hubIDs: hubIDsOf(ws)}
	for _, set := range possiblySigned(vs) {
		spec.inputs = append(spec.inputs, set...)
	}
	// It replaces whatever is relayed now, a once-confirmed version included.
	for i := len(vs) - 1; i >= 0; i-- {
		v := &vs[i]
		if v.Kind != db.CashVersionChild && v.FeeSat != nil && (v.State == db.CashVersionBroadcast || v.State == db.CashVersionSigned) {
			spec.prev = v
			break
		}
	}
	e.Invalidations.Add(1)
	return e.buildAndSign(ctx, p, tip, rates, spec)
}
