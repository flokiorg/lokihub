package apps

import (
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// PayoutState is what the node says about a redeem payout.
type PayoutState int

const (
	// PayoutUnknown: still in flight, or the node could not be asked.
	PayoutUnknown PayoutState = iota
	// PayoutFailed: failed, or the node never started it.
	PayoutFailed
	// PayoutSucceeded: paid; the lookup carries its preimage.
	PayoutSucceeded
)

// PayoutLookup asks the node about the payout with paymentHash.
type PayoutLookup func(paymentHash string) (state PayoutState, preimage string, feeMloki uint64)

// orphanRedeemClaim is a claimed slice whose payout has no preimage on it and no
// pending ledger row: nothing else will ever resolve it.
type orphanRedeemClaim struct {
	WalletAppID    uint
	PaymentHash    string
	RedeemFeeMloki int64
}

// ResolveOrphanRedeemClaims resolves every claimed slice whose redeem started
// before claimedBefore, has no preimage, and has no PENDING payment under its
// hash. Those are the claims a restart, or a failed write after the payment,
// left behind (the pending row is written only after the claim commits); they
// would otherwise stay claimed and hold an in-flight slot for good.
//
//   - The ledger shows the payment SETTLED: its preimage is recorded on the slice,
//     from the row or from the node.
//   - The ledger shows it FAILED: the slice is released, with the late-failure
//     cool-down.
//   - The ledger has no row: the node is asked. A payment it failed or never
//     started releases the slice; one it paid is recorded on the slice (the
//     ledger has no debit for it, which is logged for the operator).
//   - Anything else is left for the next run.
//
// Returns the bills a settlement was recorded on, for the drain check.
func (svc *appsService) ResolveOrphanRedeemClaims(claimedBefore time.Time, lookup PayoutLookup) ([]uint, error) {
	var orphans []orphanRedeemClaim
	err := svc.db.Model(&db.CashWalletClaim{}).
		Select("wallet_app_id, payment_hash, redeem_fee_mloki").
		Where("claimed_at IS NOT NULL AND claimed_at < ? AND payment_hash <> '' AND preimage = ''", claimedBefore).
		Where("NOT EXISTS (SELECT 1 FROM transactions WHERE transactions.app_id = cash_wallet_claims.wallet_app_id"+
			" AND transactions.type = ? AND transactions.state = ? AND transactions.payment_hash = cash_wallet_claims.payment_hash)",
			constants.TRANSACTION_TYPE_OUTGOING, constants.TRANSACTION_STATE_PENDING).
		Scan(&orphans).Error
	if err != nil {
		return nil, err
	}

	var settledOn []uint
	for _, o := range orphans {
		if redeemAnswering(o.PaymentHash) {
			continue
		}
		var rows []db.Transaction
		if err := svc.db.Where("app_id = ? AND type = ? AND payment_hash = ?",
			o.WalletAppID, constants.TRANSACTION_TYPE_OUTGOING, o.PaymentHash).Find(&rows).Error; err != nil {
			return settledOn, err
		}
		var settled, failed *db.Transaction
		for i := range rows {
			switch rows[i].State {
			case constants.TRANSACTION_STATE_SETTLED:
				settled = &rows[i]
			case constants.TRANSACTION_STATE_FAILED:
				failed = &rows[i]
			}
		}
		log := logger.Logger.With().Uint("app_id", o.WalletAppID).Str("payment_hash", o.PaymentHash).Logger()

		switch {
		case settled != nil:
			preimage, fee := "", settled.FeeMloki
			if settled.Preimage != nil {
				preimage = *settled.Preimage
			}
			if preimage == "" && lookup != nil {
				if state, p, f := lookup(o.PaymentHash); state == PayoutSucceeded {
					preimage, fee = p, f
				}
			}
			if preimage == "" {
				continue
			}
			if err := svc.RecordCashSliceRedeemSettlement(o.WalletAppID, CashSliceRedeemPayment{
				PaymentHash: o.PaymentHash, Preimage: preimage, RedeemFeeMloki: o.RedeemFeeMloki,
				RoutingFeeMloki: int64(fee), SettledAt: settled.SettledAt, //nolint:gosec // a routing fee the node reports
			}); err != nil {
				return settledOn, err
			}
			log.Info().Msg("Recorded a settled cash_redeem payout left without its preimage")
			settledOn = append(settledOn, o.WalletAppID)
		case failed != nil:
			released, err := svc.ReleaseCashSliceForFailedPayment(o.WalletAppID, o.PaymentHash)
			if err != nil {
				return settledOn, err
			}
			if released {
				log.Info().Msg("Released a cash slice whose redeem payment failed")
			}
		case lookup != nil:
			state, preimage, fee := lookup(o.PaymentHash)
			switch state {
			case PayoutFailed:
				released, err := svc.ReleaseRedeemClaim(o.WalletAppID, o.PaymentHash, nil)
				if err != nil {
					return settledOn, err
				}
				if released {
					log.Warn().Msg("Released a cash slice whose redeem payment never started")
				}
			case PayoutSucceeded:
				if preimage == "" {
					continue
				}
				now := time.Now()
				if err := svc.RecordCashSliceRedeemSettlement(o.WalletAppID, CashSliceRedeemPayment{
					PaymentHash: o.PaymentHash, Preimage: preimage, RedeemFeeMloki: o.RedeemFeeMloki,
					RoutingFeeMloki: int64(fee), SettledAt: &now, //nolint:gosec // a routing fee the node reports
				}); err != nil {
					return settledOn, err
				}
				log.Error().Msg("A cash_redeem payout was paid with no ledger row; the slice is recorded redeemed, the bill's balance needs operator attention")
			}
		}
	}
	return settledOn, nil
}
