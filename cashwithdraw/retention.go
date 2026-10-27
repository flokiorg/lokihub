package cashwithdraw

import (
	"time"

	"github.com/ohstr/nmilat/nipcash"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// VersionDropper is implemented by a payout engine that keeps versions: it
// deletes a payout's versions with its record.
type VersionDropper interface {
	DropVersionsTx(tx *gorm.DB, withdrawalID uint) error
}

// retention is a payout's retention facts.
func (s *Service) retention(tx *gorm.DB, w *db.CashWithdrawal, rec *db.CashRequestRecord) (nipcash.WithdrawRetention, error) {
	h, err := cashrequest.HorizonTx(tx)
	if err != nil {
		return nipcash.WithdrawRetention{}, err
	}
	if s.Cfg != nil {
		h = max(h, s.Cfg.PrivateEnvelopeLimits().MaxRequestAgeSeconds)
	}
	r := nipcash.WithdrawRetention{Horizon: h}
	if rec != nil {
		r.Deadline = rec.Deadline
	}
	switch {
	case w.FinalAt != nil:
		r.EndedAt = *w.FinalAt
	case w.CancelledAt != nil:
		r.EndedAt = *w.CancelledAt
	}
	var hubCfg db.CashHubConfig
	if err := tx.Where("app_id = ?", w.HubAppID).Limit(1).Find(&hubCfg).Error; err != nil {
		return r, err
	}
	r.TombstoneWindow = int64(hubCfg.SpentRetentionSecs)
	if w.RemintAppID != nil {
		var n int64
		if err := tx.Model(&db.App{}).Where("id = ?", *w.RemintAppID).Count(&n).Error; err != nil {
			return r, err
		}
		r.RemintLive = n > 0
	}
	return r, nil
}

// PruneRecords drops the withdraw records no longer retained (NIP-CASH
// §Withdrawals): the record, its bill rows, the payout row and its versions, in
// one transaction each. The archive facts stay. It returns how many it dropped.
func (s *Service) PruneRecords(limit int) (int, error) {
	var ws []db.CashWithdrawal
	if err := s.DB.Where("state IN ?", []string{db.CashWithdrawFinal, db.CashWithdrawCancelled}).
		Order("id").Limit(limit).Find(&ws).Error; err != nil {
		return 0, err
	}
	now := s.now().Unix()
	dropped := 0
	for i := range ws {
		w := &ws[i]
		var rec db.CashRequestRecord
		if err := s.DB.Where("id = ?", w.RecordID).Limit(1).Find(&rec).Error; err != nil {
			return dropped, err
		}
		r, err := s.retention(s.DB, w, &rec)
		if err != nil {
			return dropped, err
		}
		if nipcash.WithdrawRecordRetained(r, now) {
			continue
		}
		err = s.DB.Transaction(func(tx *gorm.DB) error {
			if vd, ok := s.Funds.(VersionDropper); ok {
				if err := vd.DropVersionsTx(tx, w.ID); err != nil {
					return err
				}
			}
			if rec.ID != 0 {
				if err := tx.Where("record_id = ?", rec.ID).Delete(&db.CashRequestRecordBill{}).Error; err != nil {
					return err
				}
				if err := tx.Delete(&db.CashRequestRecord{}, rec.ID).Error; err != nil {
					return err
				}
			}
			return tx.Delete(&db.CashWithdrawal{}, w.ID).Error
		})
		if err != nil {
			return dropped, err
		}
		dropped++
		logger.Logger.Debug().Uint("withdrawal_id", w.ID).Msg("Dropped a withdraw record past its retention")
	}
	return dropped, nil
}

// BlocksHubDelete reports whether hubAppID has a payout not final or cancelled,
// or a withdraw record still kept (NIP-CASH §Lifecycle and Deletion, Hub
// teardown).
func (s *Service) BlocksHubDelete(hubAppID uint) (bool, error) {
	open, kept, _, err := s.HubDeleteBlockersTx(s.DB, hubAppID, s.now())
	return open+kept > 0, err
}

// HubDeleteBlockersTx counts hubAppID's payouts not yet final or cancelled
// (open), the ended ones whose records are still kept (kept), and the time the
// last kept record goes, nil while a re-minted bill keeps one.
func (s *Service) HubDeleteBlockersTx(tx *gorm.DB, hubAppID uint, at time.Time) (open, kept int64, keptUntil *int64, err error) {
	var ws []db.CashWithdrawal
	if err := tx.Where("hub_app_id = ?", hubAppID).Find(&ws).Error; err != nil {
		return 0, 0, nil, err
	}
	now := at.Unix()
	var latest int64
	timed := true
	for i := range ws {
		var rec db.CashRequestRecord
		if err := tx.Where("id = ?", ws[i].RecordID).Limit(1).Find(&rec).Error; err != nil {
			return 0, 0, nil, err
		}
		r, err := s.retention(tx, &ws[i], &rec)
		if err != nil {
			return 0, 0, nil, err
		}
		switch {
		case !nipcash.WithdrawBlocksHubDelete(r, now):
		case r.EndedAt == 0:
			open++
		case r.RemintLive:
			kept++
			timed = false
		default:
			kept++
			latest = max(latest, r.Deadline+r.Horizon, r.EndedAt+r.TombstoneWindow)
		}
	}
	if kept > 0 && timed {
		keptUntil = &latest
	}
	return open, kept, keptUntil, nil
}

// HubDeleteBlockers is HubDeleteBlockersTx as the Cash Hub delete check reads it
// (apps.RegisterCashHubDeleteBlocker).
func (s *Service) HubDeleteBlockers(tx *gorm.DB, hubID uint, now time.Time) ([]apps.CashHubDeleteBlocker, error) {
	open, kept, until, err := s.HubDeleteBlockersTx(tx, hubID, now)
	if err != nil {
		return nil, err
	}
	return []apps.CashHubDeleteBlocker{
		{Kind: apps.CashHubBlockerPayoutOpen, Count: open},
		{Kind: apps.CashHubBlockerWithdrawRecords, Count: kept, Until: until},
	}, nil
}

// IsRemintedBill reports a bill a cancelled payout re-minted whose slice is
// still live: its owner can neither delete it nor remove its slice, as for an
// ordered slice.
func IsRemintedBill(tx *gorm.DB, appID uint) (bool, error) {
	var n int64
	err := tx.Model(&db.CashWalletClaim{}).Where("wallet_app_id = ? AND reminted = ? AND claimed_at IS NULL", appID, true).
		Count(&n).Error
	return n > 0, err
}
