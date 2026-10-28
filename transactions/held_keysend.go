package transactions

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
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
// the same checks again.
func (svc *transactionsService) resolveHeldKeysend(ctx context.Context, held *lnclient.Transaction, lnClient lnclient.LNClient) {
	log := svc.logger.With().Str("payment_hash", held.PaymentHash).Logger()
	err := svc.db.Transaction(func(tx *gorm.DB) error {
		return checkHeldKeysend(tx, svc, held)
	})
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
	amount := held.AmountPaid
	if amount <= 0 {
		amount = held.Amount
	}
	if err := checkCircleKeysendCredit(tx, recipient, nil, false, uint64(max(amount, 0))); err != nil { //nolint:gosec // non-negative
		var refusal *circlemoney.Refusal
		if errors.As(err, &refusal) {
			return errKeysendRefused
		}
		return err
	}
	return nil
}
