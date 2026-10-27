package cashwithdraw

import (
	"context"
	"encoding/hex"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
)

// leaseRenewSecs: a lease with less than this left is renewed.
const leaseRenewSecs = 7 * 86400

// coinMinConfs is the depth a coin needs to be set aside: the highest
// final_depth among open payouts and offered hubs, at least 6.
func (e *Engine) coinMinConfs() (int32, error) {
	var maxFD int
	if err := e.S.DB.Model(&db.CashWithdrawal{}).
		Where("state NOT IN ?", []string{db.CashWithdrawFinal, db.CashWithdrawCancelled}).
		Select("COALESCE(MAX(final_depth), 0)").Scan(&maxFD).Error; err != nil {
		return 0, err
	}
	var hubs []uint
	if err := e.S.DB.Model(&db.CashWithdrawTerms{}).Where("enabled = ?", true).Pluck("hub_app_id", &hubs).Error; err != nil {
		return 0, err
	}
	for _, h := range hubs {
		fd, err := e.S.FinalDepth(e.S.DB, h)
		if err != nil {
			return 0, err
		}
		maxFD = max(maxFD, fd)
	}
	return int32(max(maxFD, MinFinalDepth)), nil //nolint:gosec // small
}

// coinStep is step 3: the wallet's coins at final depth, less those paid to
// order addresses and those another lease holds, upserted as available; a coin
// gone from the wallet and spent by none of our versions is vanished (replaced
// before its payout's first signature); the floor read; set-aside coins leased
// and re-leased; set-asides short of coins topped up.
func (e *Engine) coinStep(ctx context.Context, p lnclient.OnchainPayer, tip int64) error {
	minConfs, err := e.coinMinConfs()
	if err != nil {
		return err
	}
	utxos, err := p.ListUnspent(ctx, minConfs, "")
	if err != nil {
		return err
	}
	leases, err := p.ListLeases(ctx)
	if err != nil {
		return err
	}
	id, err := e.lease()
	if err != nil {
		return err
	}
	foreign := map[string]bool{}
	for _, l := range leases {
		if l.LeaseID != id {
			foreign[l.Outpoint] = true
		}
	}
	reserve, err := p.RequiredReserve(ctx)
	if err != nil {
		return err
	}
	e.floorSat.Store(e.env().FloorReserveMult() * reserve)
	now := e.S.now().Unix()

	seen := map[string]bool{}
	for _, u := range utxos {
		if foreign[u.Outpoint] {
			continue
		}
		if own, err := e.S.orders().IsOrderScript(e.S.DB, u.Script); err != nil {
			return err
		} else if own {
			continue // never pay withdrawals from order deposits
		}
		seen[u.Outpoint] = true
		row := db.CashWithdrawCoin{Outpoint: u.Outpoint, ValueSat: u.ValueSat, Script: hex.EncodeToString(u.Script),
			ConfHeight: tip - u.Confs + 1, State: db.CashCoinAvailable, SeenAt: now}
		if err := e.S.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "outpoint"}},
			DoUpdates: clause.AssignmentColumns([]string{"seen_at"})}).Create(&row).Error; err != nil {
			return err
		}
	}

	// Coins we hold as available or set aside that the wallet no longer lists.
	var ours []db.CashWithdrawCoin
	if err := e.S.DB.Where("state IN ?", []string{db.CashCoinAvailable, db.CashCoinSetAside}).Find(&ours).Error; err != nil {
		return err
	}
	for _, c := range ours {
		if seen[c.Outpoint] {
			continue
		}
		state := db.CashCoinVanished
		if spentByVersion, err := e.spentByOurVersion(c.Outpoint); err != nil {
			return err
		} else if spentByVersion {
			state = db.CashCoinInVersion
		}
		if state == db.CashCoinVanished && c.State == db.CashCoinAvailable && !foreign[c.Outpoint] {
			// An available coin the wallet stopped listing: spent elsewhere,
			// or leased by someone else now. Not ours to use.
			if err := e.S.DB.Where("outpoint = ? AND state = ?", c.Outpoint, db.CashCoinAvailable).Delete(&db.CashWithdrawCoin{}).Error; err != nil {
				return err
			}
			continue
		}
		if state == db.CashCoinVanished {
			e.Vanished.Add(1)
			logger.Logger.Error().Str("outpoint", c.Outpoint).Msg("A coin set aside for a payout vanished from the wallet")
		}
		if err := e.S.DB.Model(&db.CashWithdrawCoin{}).Where("outpoint = ? AND state = ?", c.Outpoint, c.State).
			Updates(map[string]interface{}{"state": state, "vanished_at": now, "vanished_height": tip}).Error; err != nil {
			return err
		}
	}

	// Top up set-asides whose coins fell short (a vanished coin is replaced
	// before the payout's first signature).
	var asides []db.CashWithdrawSetAside
	if err := e.S.DB.Find(&asides).Error; err != nil {
		return err
	}
	for _, a := range asides {
		var w db.CashWithdrawal
		if err := e.S.DB.Where("id = ?", a.WithdrawalID).Limit(1).Find(&w).Error; err != nil {
			return err
		}
		if w.ID == 0 || w.Signed {
			continue
		}
		if err := e.S.DB.Transaction(func(tx *gorm.DB) error {
			if err := lockTx(tx, w.HubAppID); err != nil {
				return err
			}
			return assignCoinsTx(tx, a.WithdrawalID, a.AmountSat)
		}); err != nil {
			return err
		}
	}
	return e.leaseCoins(ctx, p, id, false)
}

// leaseCoins leases every coin set aside or in a version of an open batch at
// the node under the engine's lease id, renewing a lease with less than 7 days
// left (always, when all).
func (e *Engine) leaseCoins(ctx context.Context, p lnclient.OnchainPayer, id [32]byte, all bool) error {
	now := e.S.now().Unix()
	var coins []db.CashWithdrawCoin
	q := e.S.DB.Where("state IN ?", []string{db.CashCoinSetAside, db.CashCoinInVersion})
	if !all {
		q = q.Where("leased_until IS NULL OR leased_until < ?", now+leaseRenewSecs)
	}
	if err := q.Find(&coins).Error; err != nil {
		return err
	}
	for _, c := range coins {
		exp, err := p.LeaseOutput(ctx, id, c.Outpoint, e.env().LeaseSecs())
		if err != nil {
			logger.Logger.Warn().Err(err).Str("outpoint", c.Outpoint).Msg("Could not lease a set-aside coin; retried next pass")
			continue
		}
		if err := e.S.DB.Model(&db.CashWithdrawCoin{}).Where("outpoint = ?", c.Outpoint).Update("leased_until", exp).Error; err != nil {
			return err
		}
	}
	return nil
}

// spentByOurVersion reports an outpoint an input of one of our versions.
func (e *Engine) spentByOurVersion(outpoint string) (bool, error) {
	var vs []db.CashWithdrawVersion
	if err := e.S.DB.Where("state <> ?", db.CashVersionInvalid).Select("inputs").Find(&vs).Error; err != nil {
		return false, err
	}
	for i := range vs {
		for _, in := range inputsOf(&vs[i]) {
			if in == outpoint {
				return true, nil
			}
		}
	}
	return false, nil
}

// SpendCheck is what an operator's own on-chain spend may take (the
// RedeemOnchainFunds guard): at most the confirmed balance less the floor and
// everything set aside, and no send-all while anything is set aside.
func (e *Engine) SpendCheck(ctx context.Context, amountSat uint64, sendAll bool) error {
	var aside int64
	if err := e.S.DB.Model(&db.CashWithdrawSetAside{}).Select("COALESCE(SUM(amount_sat), 0)").Scan(&aside).Error; err != nil {
		return err
	}
	if sendAll && aside > 0 {
		return ErrSpendSetAside
	}
	snap, err := e.Snapshot(ctx)
	if err != nil {
		return err
	}
	if int64(amountSat) > snap.ConfirmedSat-e.floorSat.Load()-aside { //nolint:gosec // bounded
		return ErrSpendSetAside
	}
	return nil
}

// SpendChecker is a payout engine that guards the operator's own spends.
type SpendChecker interface {
	SpendCheck(ctx context.Context, amountSat uint64, sendAll bool) error
}

// OnchainSpendCheck refuses an operator spend of amountSat (or a send-all) that
// would take coins set aside for payouts; without an engine it allows it.
func (s *Service) OnchainSpendCheck(ctx context.Context, amountSat uint64, sendAll bool) error {
	if sc, ok := s.Funds.(SpendChecker); ok && sc != nil {
		return sc.SpendCheck(ctx, amountSat, sendAll)
	}
	return nil
}
