package cashwithdraw

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ohstr/nmilat/nipcash"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/ledger"
	"github.com/flokiorg/lokihub/logger"
)

// ErrAlreadyReminted: the payout was re-minted before (the re-mint is a no-op).
var ErrAlreadyReminted = errors.New("this payout was already re-minted")

// RemintDeps are the services a re-mint commit needs to create a bill.
func (s *Service) remintDeps(ctx context.Context) (cashwallet.Deps, error) {
	if s.LN == nil && s.NodeOverride == nil {
		return cashwallet.Deps{}, errors.New("the node is not started")
	}
	d := cashwallet.Deps{AppsService: s.Apps, Keys: s.Keys, DB: s.DB, IAChecker: s.IA, Now: s.now}
	if s.LN != nil {
		d.LNClient = s.LN()
	}
	if s.Cfg != nil {
		d.RelayURLs = s.Cfg.GetRelayUrls()
	}
	return d, nil
}

// Remint cancels a payout and re-mints its whole amount as a new bill for the
// same holder, in one commit (NIP-CASH §Cancelling a Payout). The payout engine
// calls it, and only once no version of the payout can confirm. It is
// idempotent: a payout already cancelled, or whose hold is gone, is
// ErrAlreadyReminted and nothing is written; the credit's unique key makes a
// retried re-mint pay once.
//
// The new bill is bound to the slice's registered identity or, for a cash-mode
// slice, to remint_commitment (never the old commitment), with the slice's fee
// terms, its ordered mark, and RemintExpiry's expiry; it is funded from the
// hold, never from the source bill. The source slice's terminal state becomes
// transferred, on the live claim or on its archive (and on the bill archive's
// reason when that slice gave it). The live-wallet cap and the lineage limiter
// do not apply: the Hub owes it.
func (s *Service) Remint(ctx context.Context, withdrawalID uint, reason string) (*db.CashWithdrawal, error) {
	deps, err := s.remintDeps(ctx)
	if err != nil {
		return nil, err
	}
	var w db.CashWithdrawal
	if err := s.DB.First(&w, withdrawalID).Error; err != nil {
		return nil, err
	}
	var hub db.App
	if err := s.DB.First(&hub, w.HubAppID).Error; err != nil {
		return nil, fmt.Errorf("the payout's Cash Hub: %w", err)
	}
	var created *cashwallet.RemintedBill
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		created = nil
		if err := lockTx(tx, w.HubAppID, w.BillAppID); err != nil {
			return err
		}
		if err := s.beforeRemint(StageAfterLock); err != nil {
			return err
		}
		var cur db.CashWithdrawal
		if err := tx.First(&cur, withdrawalID).Error; err != nil {
			return err
		}
		if cur.State == db.CashWithdrawCancelled || cur.HeldMillis != cur.AmountMillis {
			return ErrAlreadyReminted
		}
		now := s.now()
		identityType, identityValue := cur.IdentityType, cur.IdentityValue
		if identityType == db.CashIdentityCash {
			if cur.RemintCommitment == nil {
				return errors.New("a cash-mode payout without its re-mint commitment")
			}
			identityValue = *cur.RemintCommitment
		}
		var hubCfg db.CashHubConfig
		if err := tx.Where("app_id = ?", cur.HubAppID).Limit(1).Find(&hubCfg).Error; err != nil {
			return err
		}
		var def *int64
		if hubCfg.MaxExpSecs > 0 {
			v := int64(hubCfg.MaxExpSecs)
			def = &v
		}
		var expiresAt *time.Time
		if e := nipcash.RemintExpiry(cur.SliceExpiresAt, now.Unix(), def); e != nil {
			t := time.Unix(*e, 0)
			expiresAt = &t
		}
		bill, err := cashwallet.CreateRemintedBillTx(ctx, tx, deps, cashwallet.RemintBill{
			Hub: &hub, NameSeed: "remint-" + cur.RequestID[:8], AmountMillis: uint64(cur.AmountMillis), //nolint:gosec // positive
			ExpiresAt: expiresAt, IdentityType: identityType, IdentityValue: identityValue, IAPubkey: cur.IAPubkey,
			MinTransferMillis: cur.MinTransferMillis, RedeemFeePpm: cur.RedeemFeePpm, RedeemFeeBaseMillis: cur.RedeemFeeBaseMillis,
		})
		if err != nil {
			return err
		}
		if err := s.beforeRemint(StageAfterCreateApp); err != nil {
			return err
		}
		if err := s.beforeRemint(StageAfterSign); err != nil {
			return err
		}
		if err := s.beforeRemint(StageAfterClaims); err != nil {
			return err
		}
		if err := ledger.HeldCreditTx(tx, bill.App.ID, uint64(cur.AmountMillis), "cash_withdraw_remint", "wd-remint:"+cur.RequestID); err != nil { //nolint:gosec // positive
			if errors.Is(err, ledger.ErrDuplicateCredit) {
				return ErrAlreadyReminted
			}
			return err
		}
		if err := s.beforeRemint(StageAfterCredit); err != nil {
			return err
		}
		if err := rewriteSourceTx(tx, &cur); err != nil {
			return err
		}
		if err := s.beforeRemint(StageAfterSourceRewrite); err != nil {
			return err
		}
		due, err := noticeDue(now)
		if err != nil {
			return err
		}
		updates := map[string]interface{}{
			"state": db.CashWithdrawCancelled, "held_millis": 0, "cancelled_at": now.Unix(), "state_at": now.Unix(),
			"remint_app_id": bill.App.ID, "remint_wallet_pubkey": *bill.App.WalletPubkey, "notice_due_at": due,
		}
		if cur.CancelReason == nil && reason != "" {
			updates["cancel_reason"] = reason
		}
		res := tx.Model(&db.CashWithdrawal{}).
			Where("id = ? AND state <> ? AND held_millis = amount_millis", cur.ID, db.CashWithdrawCancelled).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrAlreadyReminted
		}
		cur.State = db.CashWithdrawCancelled
		cur.Txid, cur.Vout = nil, nil
		if err := appendEventTx(tx, &cur, now.Unix(), nil); err != nil {
			return err
		}
		rb := bill.Record
		rb.RecordID, rb.Role, rb.Ordinal = cur.RecordID, RemintRole, 0
		if err := tx.Create(&rb).Error; err != nil {
			return err
		}
		if err := s.beforeRemint(StageAfterRecord); err != nil {
			return err
		}
		if err := s.beforeRemint(StageBeforeCommit); err != nil {
			return err
		}
		created = bill
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.Apps.NotifyAppCreated(created.App)
	logger.Logger.Info().Uint("withdrawal_id", w.ID).Uint("remint_app_id", created.App.ID).Msg("cash_withdraw payout cancelled and re-minted")
	var out db.CashWithdrawal
	if err := s.DB.First(&out, w.ID).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

// rewriteSourceTx turns the source slice's terminal state redeemed into
// transferred: on the live claim, or on its archive row and, when that slice
// gave the bill archive its reason, on the reason (NIP-CASH §Cancelling a
// Payout; the only terminal change allowed).
func rewriteSourceTx(tx *gorm.DB, w *db.CashWithdrawal) error {
	res := tx.Model(&db.CashWalletClaim{}).Where("id = ? AND ended_as = ?", w.ClaimID, db.CashSliceStatusRedeemed).
		Update("ended_as", db.CashSliceStatusTransferred)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 1 {
		return nil
	}
	var slice db.CashBillSliceArchive
	if err := tx.Where("claim_id = ? AND wallet_app_id = ?", w.ClaimID, w.BillAppID).Limit(1).Find(&slice).Error; err != nil {
		return err
	}
	if slice.ID == 0 {
		return nil
	}
	if err := tx.Model(&db.CashBillSliceArchive{}).Where("id = ? AND outcome = ?", slice.ID, db.CashSliceStatusRedeemed).
		Update("outcome", db.CashSliceStatusTransferred).Error; err != nil {
		return err
	}
	// The bill's reason came from its last claim to end; rewrite it only when
	// that is the withdrawn slice.
	var last db.CashBillSliceArchive
	if err := tx.Where("wallet_app_id = ? AND claimed_at IS NOT NULL", w.BillAppID).
		Order("claimed_at DESC, id DESC").Limit(1).Find(&last).Error; err != nil {
		return err
	}
	if last.ID != slice.ID {
		return nil
	}
	transferred := nipcash.TombstoneReason(db.CashSliceStatusTransferred)
	return tx.Model(&db.CashBillArchive{}).
		Where("wallet_app_id = ? AND reason = ?", w.BillAppID, nipcash.TombstoneReason(db.CashSliceStatusRedeemed)).
		Update("reason", transferred).Error
}
