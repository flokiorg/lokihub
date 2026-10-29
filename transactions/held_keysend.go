package transactions

import (
	"context"
	"errors"
	"github.com/flokiorg/lokihub/hubclock"
	"sync"
	"time"

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
		svc.heldKeysends.release(held.PaymentHash)
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
			svc.heldKeysends.release(held.PaymentHash)
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
	forWallet, forHub := svc.heldKeysends.reserved(recipient.ID, hubID, held.PaymentHash)
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
	// Reserved under the Hub's lock, before it is released.
	svc.heldKeysends.reserve(held.PaymentHash, recipient.ID, hubID, amount)
	return nil
}

// heldKeysendTTL bounds a reservation whose credit never arrives (the settle
// event lost): the node's own settle has happened or failed by then.
const heldKeysendTTL = 10 * time.Minute

// keysendReservations are held keysends to Circle Wallets that passed their
// checks and are being settled, by payment hash, until the node's settle event
// credits them.
type keysendReservations struct {
	mu   sync.Mutex
	byID map[string]keysendReservation
}

type keysendReservation struct {
	wallet, hub uint
	amount      uint64
	at          time.Time
}

func newKeysendReservations() *keysendReservations {
	return &keysendReservations{byID: map[string]keysendReservation{}}
}

func (r *keysendReservations) reserve(hash string, wallet, hub uint, amount uint64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[hash] = keysendReservation{wallet: wallet, hub: hub, amount: amount, at: time.Now()}
}

func (r *keysendReservations) release(hash string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, hash)
}

// reserved sums the live reservations for wallet and for its circle, other than
// hash's own.
func (r *keysendReservations) reserved(wallet, hub uint, hash string) (forWallet, forHub uint64) {
	if r == nil {
		return 0, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for h, res := range r.byID {
		if now.Sub(res.at) > heldKeysendTTL {
			delete(r.byID, h)
			continue
		}
		if h == hash || res.hub != hub {
			continue
		}
		forHub += res.amount
		if res.wallet == wallet {
			forWallet += res.amount
		}
	}
	return forWallet, forHub
}
