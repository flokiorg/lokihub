package transactions

import (
	"context"
	"errors"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/hubclock"
	"github.com/flokiorg/lokihub/lnclient"
)

// hasIncomingRow reports whether an incoming row of ours names paymentHash: an
// invoice lokihub issued, never a keysend.
func (svc *transactionsService) hasIncomingRow(paymentHash string) bool {
	var n int64
	if err := svc.db.Model(&db.Transaction{}).
		Where("payment_hash = ? AND type = ?", paymentHash, constants.TRANSACTION_TYPE_INCOMING).Count(&n).Error; err != nil {
		return true
	}
	return n > 0
}

// errKeysendRefused: the keysend names a Circle Wallet whose checks refuse it.
var errKeysendRefused = errors.New("the keysend is refused by the circle wallet's checks")

// resolveHeldKeysend settles or fails a keysend the node holds at the final hop.
// One that names a Circle Wallet must pass the wallet's checks (Active, its
// receive room, its circle's holdings ceiling, under the Hub's lock) or it is
// failed: NIP-CW §Own Funds and Caps requires failing, never crediting, an
// over-room external payment with no invoice and every payment to a wallet that
// is not Active. Every other keysend is settled, as the node would.
//
// The settled keysend is then credited by the node's settle event, which runs
// the same checks again. Between the settle and that credit its amount is
// reserved against the wallet's room and the circle's ceiling, so a second
// keysend checked meanwhile cannot also pass and then be left with the host
// (R2-F2).
func (svc *transactionsService) resolveHeldKeysend(ctx context.Context, held *lnclient.Transaction, lnClient lnclient.LNClient) {
	log := svc.logger.With().Str("payment_hash", held.PaymentHash).Logger()
	err := svc.db.Transaction(func(tx *gorm.DB) error {
		return checkHeldKeysend(tx, svc, held)
	})
	if err != nil || held.Preimage == "" {
		svc.releaseHeldKeysend(held.PaymentHash)
	}
	switch {
	case errors.Is(err, errKeysendRefused):
		if cancelErr := lnClient.CancelHoldInvoice(ctx, held.PaymentHash); cancelErr != nil {
			log.Error().Err(cancelErr).Msg("Failed to fail a refused keysend; the node fails it when its hold time ends")
			return
		}
		log.Info().Msg("Failed a keysend the circle wallet's checks refuse")
	case err != nil:
		log.Error().Err(err).Msg("Failed to check a held keysend; the node fails it when its hold time ends")
	case held.Preimage == "":
		log.Error().Msg("A held keysend has no preimage; the node fails it when its hold time ends")
	default:
		if settleErr := lnClient.SettleHoldInvoice(ctx, held.Preimage); settleErr != nil {
			svc.releaseHeldKeysend(held.PaymentHash)
			log.Error().Err(settleErr).Msg("Failed to settle a held keysend")
		}
	}
}

// checkHeldKeysend returns errKeysendRefused when held names a Circle Wallet
// that may not take it.
func checkHeldKeysend(tx *gorm.DB, svc *transactionsService, held *lnclient.Transaction) error {
	records, _ := held.Metadata["tlv_records"].([]lnclient.TLVRecord)
	appID := svc.findCustomKeyApp(records, tx)
	recipient, err := loadApp(tx, appID)
	if err != nil || !circlemoney.IsCircleWallet(recipient) {
		return nil
	}
	if err := db.LockApp(tx, *recipient.ParentAppID); err != nil {
		return err
	}
	amount := uint64(max(held.AmountPaid, 0)) //nolint:gosec // non-negative
	if amount == 0 {
		amount = uint64(max(held.Amount, 0)) //nolint:gosec // non-negative
	}
	hubID := *recipient.ParentAppID
	// Keysends settled but not yet credited count as received.
	forWallet, forHub, err := circlelife.HeldKeysendsReserved(tx, recipient.ID, hubID, held.PaymentHash, time.Now())
	if err != nil {
		return err
	}
	refused := func(err error) error {
		var refusal *circlemoney.Refusal
		if errors.As(err, &refusal) {
			return errKeysendRefused
		}
		return err
	}
	if err := checkCircleKeysendCredit(tx, recipient, nil, false, amount+forWallet); err != nil {
		return refused(err)
	}
	if forHub > forWallet {
		cfg, err := circlemoney.HubConfig(tx, hubID)
		if err != nil {
			return err
		}
		if err := circlemoney.CheckHoldings(tx, hubID, cfg, amount+forHub, hubclock.Now()); err != nil {
			return refused(err)
		}
	}
	// Reserved under the Hub's lock, in the checking transaction; lapsed
	// reservations go.
	if err := tx.Where("reserved_at < ?", time.Now().Add(-circlelife.HeldKeysendTTL).Unix()).Delete(&db.CircleHeldKeysend{}).Error; err != nil {
		return err
	}
	return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&db.CircleHeldKeysend{PaymentHash: held.PaymentHash,
		WalletAppID: recipient.ID, HubAppID: hubID, AmountMloki: int64(min(amount, math.MaxInt64)), ReservedAt: time.Now().Unix()}).Error //nolint:gosec // bounded
}

// releaseHeldKeysend ends a held keysend's reservation that will not be
// credited (refused, or its settle failed).
func (svc *transactionsService) releaseHeldKeysend(paymentHash string) {
	if err := svc.db.Where("payment_hash = ?", paymentHash).Delete(&db.CircleHeldKeysend{}).Error; err != nil {
		svc.logger.Error().Err(err).Str("payment_hash", paymentHash).Msg("Failed to release a held keysend's reservation; it lapses")
	}
}
