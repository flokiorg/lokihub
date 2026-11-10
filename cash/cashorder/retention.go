package cashorder

import (
	"github.com/ohstr/nmilat/nipCO"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// RunRetention drops the orders NIP-CO lets a Hub drop (the 5-min job): an
// unpaid Lightning order once pay_by and deadline + H passed, a Lightning
// order once none of its bills is live or tombstoned and that minimum passed,
// an unpaid on-chain order at nipCO.UnpaidOnchainDeleteAt. Each goes with its
// record, and an unpaid one with its used request_id mark once
// nipCO.UsedMarkDroppable holds. An on-chain order with a payment is kept for
// as long as the Hub runs; an unpaid one's address stays burnt.
//
// pay_by is the Hub's and is judged on its clock; the deadline + H floor is
// the client's deadline and is judged on the wall clock, as its commit and
// its lookup judge it, so a record and its mark outlive every resend of it.
func (e *Engine) RunRetention() {
	now := e.now()
	wall := e.wall(now).Unix()
	h := e.horizon()
	var orders []db.CashOrder
	if err := e.DB.Where("state IN ? AND pay_by < ?", []string{db.CashOrderExpired, db.CashOrderMinted}, now.Unix()).
		Find(&orders).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to list orders for retention")
		return
	}
	for i := range orders {
		o := &orders[i]
		err := e.DB.Transaction(func(tx *gorm.DB) error {
			if err := db.LockNamespace(tx, db.LockNsOrder, int32(o.HubAppID)); err != nil { //nolint:gosec // app ids are small
				return err
			}
			r, _, err := apps.OrderRetentionTx(tx, o, h, now)
			if err != nil {
				return err
			}
			// Past deadline + H on the wall clock (UnpaidOnchainDeleteAt and
			// OrderRetained's deadline floor).
			pastFloor := wall > o.Deadline+h
			drop := false
			switch o.Payment {
			case nipCO.PaymentOnchain:
				drop = !r.Seen && now.Unix() >= o.PayBy && wall >= o.Deadline+h
			case nipCO.PaymentLightning:
				drop = pastFloor && !nipCO.OrderRetained(r, now.Unix())
			}
			if !drop {
				return nil
			}
			return apps.DropOrderTx(tx, o, nipCO.UsedMarkDroppable(o.Deadline, h, wall, r.Seen))
		})
		if err != nil {
			logger.Logger.Error().Err(err).Uint("order_id", o.ID).Msg("Failed to drop an order")
		}
	}
}
