package cashorder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flokiorg/lokihub/hubclock"
	"strings"
	"sync"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipCO"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/ledger"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/transactions"
)

// NoticePublisher publishes an order notice on relay, the relay its order
// arrived on, falling back to the Hub's other relays only if it refuses.
type NoticePublisher func(ctx context.Context, relay string, ev *nip01.Event) error

// Engine takes order payments in and mints them (NIP-CO §Paying and Minting),
// runs the pay_by, chain and retention jobs, and publishes order notices.
type Engine struct {
	DB           *gorm.DB
	Keys         keys.Keys
	Apps         apps.AppsService
	Transactions transactions.TransactionsService
	LN           func() lnclient.LNClient
	Relays       func() []string
	// Horizon is the H in force (NIP-CASH §Deadlines).
	Horizon func() int64
	Now     func() time.Time

	mu       sync.Mutex
	notices  NoticePublisher
	mintLock sync.Mutex
}

// Retry backoff of a payment whose bill could not be built: 30 s doubling to
// 10 min, never giving up (NIP-CO §Minting).
const (
	mintRetryMin = 30 * time.Second
	mintRetryMax = 10 * time.Minute
	// droppedAfterMisses: an unconfirmed output absent from this many
	// consecutive chain scans is dropped.
	droppedAfterMisses = 2
)

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return hubclock.Now()
}

func (e *Engine) horizon() int64 {
	if e.Horizon != nil {
		return e.Horizon()
	}
	h, _ := cashrequest.HorizonTx(e.DB)
	return h
}

// SetNoticePublisher sets how notices reach the relays; none are published
// before it is set (a lost notice is never retried: clients poll).
func (e *Engine) SetNoticePublisher(p NoticePublisher) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notices = p
}

func (e *Engine) walletDeps(ln lnclient.LNClient) cashwallet.Deps {
	var relays []string
	if e.Relays != nil {
		relays = e.Relays()
	}
	return cashwallet.Deps{AppsService: e.Apps, TransactionsService: e.Transactions, LNClient: ln, Keys: e.Keys,
		DB: e.DB, RelayURLs: relays}
}

// notify publishes an order notice for order (NIP-CO §Order Notice), after the
// commit that changed it. A notice that cannot be published is logged only.
func (e *Engine) notify(order *db.CashOrder) {
	e.mu.Lock()
	publish := e.notices
	e.mu.Unlock()
	if publish == nil || order == nil {
		return
	}
	ev, err := nipCO.NewOrderNotice(order.Notify)
	if err != nil {
		logger.Logger.Warn().Err(err).Uint("order_id", order.ID).Msg("Could not build an order notice")
		return
	}
	relay := order.ArrivalRelay
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := publish(ctx, relay, ev); err != nil {
			logger.Logger.Debug().Err(err).Uint("order_id", order.ID).Msg("Could not publish an order notice")
		}
	}()
}

// lightningKey and onchainKey are a payment's key: what mints it once.
func lightningKey(paymentHash string) string { return "ln:" + paymentHash }
func onchainKey(txid string, vout uint32) string {
	return fmt.Sprintf("oc:%s:%d", txid, vout)
}

// chainConfirmations reads an on-chain payment's confirmations from the node:
// 0 when its output is no longer in the wallet's view. ok is false for a
// payment that is not on-chain, or when the node cannot be asked.
func (e *Engine) chainConfirmations(ctx context.Context, ln lnclient.LNClient, paymentID uint) (int, bool) {
	var p db.CashOrderPayment
	if err := e.DB.Select("payment_key").First(&p, paymentID).Error; err != nil || !strings.HasPrefix(p.PaymentKey, "oc:") {
		return 0, false
	}
	lister, ok := ln.(lnclient.OnchainOutputLister)
	if !ok {
		return 0, false
	}
	outputs, err := lister.ListOnchainOutputs(ctx)
	if err != nil {
		logger.Logger.Warn().Err(err).Msg("Could not read an order payment's confirmations; its mint waits")
		return 0, true
	}
	for _, o := range outputs {
		if onchainKey(o.TxID, o.Vout) == p.PaymentKey {
			return int(o.Confirmations), true
		}
	}
	return 0, true
}

// --- Lightning intake ---

// IntakeLightning takes a settled invoice in: when its payment hash names an
// order, the payment is recorded once (its key is unique), the order is paid,
// and it is minted; the invoice then bypasses app crediting (handled true).
// received is what the payer paid, so an overpayment mints more.
func (e *Engine) IntakeLightning(ctx context.Context, inv *lnclient.Transaction) bool {
	if inv == nil || inv.PaymentHash == "" || inv.SettledAt == nil {
		return false
	}
	var order db.CashOrder
	err := e.DB.Where("payment_hash = ?", inv.PaymentHash).Limit(1).Find(&order).Error
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to look up an order by payment hash")
		return false
	}
	if order.ID == 0 {
		return false
	}
	received := inv.AmountPaid
	if received <= 0 {
		received = inv.Amount
	}
	hub := order.HubAppID
	payment := db.CashOrderPayment{OrderID: &order.ID, HubAppID: &hub, PaymentKey: lightningKey(inv.PaymentHash),
		ValueMillis: received, SeenAt: e.now().Unix(), Confirmations: 1, State: db.CashOrderPaymentConfirmed}
	inserted, err := e.insertPayment(&payment, &order)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("order_id", order.ID).Msg("Failed to record an order payment; the startup sweep reads it again")
		return true
	}
	if inserted {
		logger.Logger.Info().Uint("order_id", order.ID).Int64("value_millis", received).Msg("An order's invoice was paid")
	}
	e.mintPending(ctx, order.ID)
	return true
}

// insertPayment records p once, under the Hub's order lock, and states its
// order again (paid); inserted is false when p was already recorded.
func (e *Engine) insertPayment(p *db.CashOrderPayment, order *db.CashOrder) (inserted bool, err error) {
	var changed *db.CashOrder
	err = e.DB.Transaction(func(tx *gorm.DB) error {
		if order != nil {
			if err := db.LockNamespace(tx, db.LockNsOrder, int32(order.HubAppID)); err != nil { //nolint:gosec // app ids are small
				return err
			}
		}
		var n int64
		if err := tx.Model(&db.CashOrderPayment{}).Where("payment_key = ?", p.PaymentKey).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		if err := tx.Create(p).Error; err != nil {
			if db.IsDuplicateKeyError(err) {
				return nil
			}
			return err
		}
		inserted = true
		if order != nil {
			o, err := e.restateTx(tx, order.ID)
			if err != nil {
				return err
			}
			changed = o
		}
		return nil
	})
	if err == nil && changed != nil {
		e.notify(changed)
	}
	return inserted, err
}

// restateTx states order orderID again from its payments at now (NIP-CO §Order
// State) and releases its reservation once it is minted, expired or closed.
// It returns the order when its state changed, nil otherwise.
func (e *Engine) restateTx(tx *gorm.DB, orderID uint) (*db.CashOrder, error) {
	var order db.CashOrder
	if err := tx.First(&order, orderID).Error; err != nil {
		return nil, err
	}
	facts, err := orderFactsTx(tx, &order)
	if err != nil {
		return nil, err
	}
	state := string(nipCO.OrderStateAt(facts, e.now().Unix()))
	// closed is final (NIP-CO §Order State): a later payment still mints, and
	// nothing turns it back.
	if order.State == db.CashOrderClosed {
		state = db.CashOrderClosed
	}
	reserved := order.ReservedMillis
	if state == db.CashOrderMinted || state == db.CashOrderExpired || state == db.CashOrderClosed {
		reserved = 0
	}
	if state == order.State && reserved == order.ReservedMillis {
		return nil, nil
	}
	if err := tx.Model(&db.CashOrder{}).Where("id = ?", order.ID).
		Updates(map[string]interface{}{"state": state, "reserved_millis": reserved, "state_at": e.now().Unix()}).Error; err != nil {
		return nil, err
	}
	changed := state != order.State
	order.State, order.ReservedMillis = state, reserved
	if !changed {
		return nil, nil
	}
	return &order, nil
}

func orderFactsTx(tx *gorm.DB, order *db.CashOrder) (nipCO.OrderFacts, error) {
	var payments []db.CashOrderPayment
	if err := tx.Where("order_id = ?", order.ID).Find(&payments).Error; err != nil {
		return nipCO.OrderFacts{}, err
	}
	f := nipCO.OrderFacts{Payment: order.Payment, PayBy: order.PayBy}
	for _, p := range payments {
		switch p.State {
		case db.CashOrderPaymentSeen, db.CashOrderPaymentConfirmed:
			f.Seen = true
			f.Unminted++
		case db.CashOrderPaymentMinted, db.CashOrderPaymentReorged:
			f.Seen = true
			f.Minted++
		case db.CashOrderPaymentKept:
			f.Seen = true
		case db.CashOrderPaymentDropped:
			// Gone from the mempool and the chain, but it was seen: an
			// on-chain order that saw it by pay_by is kept (NIP-CO §On-chain).
			f.Seen = true
		}
	}
	return f, nil
}

// --- Minting ---

// mintPending mints every confirmed, unminted payment of orderID whose retry
// is due.
func (e *Engine) mintPending(ctx context.Context, orderID uint) {
	var ids []uint
	if err := e.DB.Model(&db.CashOrderPayment{}).
		Where("order_id = ? AND state = ? AND next_attempt_at <= ?", orderID, db.CashOrderPaymentConfirmed, e.now().Unix()).
		Pluck("id", &ids).Error; err != nil {
		logger.Logger.Error().Err(err).Uint("order_id", orderID).Msg("Failed to list an order's payments to mint")
		return
	}
	for _, id := range ids {
		if err := e.MintPayment(ctx, id); err != nil {
			logger.Logger.Error().Err(err).Uint("payment_id", id).Msg("Failed to mint an order payment; retrying")
		}
	}
}

// MintPayment mints one confirmed payment in one commit (NIP-CO §Minting): the
// Hub is credited with what it brought in, then each bill of
// nipCO.MintSplit(received, fee_millis, max_millis) is created, signed and
// funded from it, recorded on the order's record, and the payment is minted. A
// payment of at most the fee mints nothing and is kept. A failure rolls it all
// back and schedules a retry.
func (e *Engine) MintPayment(ctx context.Context, paymentID uint) error {
	e.mintLock.Lock()
	defer e.mintLock.Unlock()
	ln := e.LN()
	if ln == nil {
		return e.mintFailed(paymentID, errors.New("the node is not started"))
	}
	deps := e.walletDeps(ln)
	// An on-chain payment's confirmations are read from the chain now, not
	// taken from the last scan: a reorganisation since then is seen (NIP-CO
	// §On-chain). The node is asked outside the transaction.
	fresh, freshOK := e.chainConfirmations(ctx, ln, paymentID)
	var minted []cashwallet.MintedBill
	var changed *db.CashOrder
	err := e.DB.Transaction(func(tx *gorm.DB) error {
		var p db.CashOrderPayment
		if err := tx.First(&p, paymentID).Error; err != nil {
			return err
		}
		if p.State != db.CashOrderPaymentConfirmed || p.OrderID == nil {
			return nil
		}
		var order db.CashOrder
		if err := tx.First(&order, *p.OrderID).Error; err != nil {
			return err
		}
		if err := db.LockApp(tx, order.HubAppID); err != nil {
			return err
		}
		if err := db.LockNamespace(tx, db.LockNsOrder, int32(order.HubAppID)); err != nil { //nolint:gosec // app ids are small
			return err
		}
		if err := tx.First(&p, paymentID).Error; err != nil || p.State != db.CashOrderPaymentConfirmed {
			return err
		}
		var hub db.App
		if err := tx.First(&hub, order.HubAppID).Error; err != nil {
			return err
		}
		var terms orderTerms
		if err := json.Unmarshal([]byte(order.TermsJSON), &terms); err != nil {
			return err
		}
		value := uint64(max(p.ValueMillis, 0)) //nolint:gosec // non-negative
		// An on-chain payment is minted only at its tier's confirmations, read
		// again here: a reorganisation since it was confirmed leaves it unminted
		// (NIP-CO §On-chain).
		if strings.HasPrefix(p.PaymentKey, "oc:") {
			confs := p.Confirmations
			if freshOK {
				confs = fresh
			}
			if confs < requiredConfirmations(&order, value) {
				return tx.Model(&db.CashOrderPayment{}).Where("id = ? AND state = ?", p.ID, db.CashOrderPaymentConfirmed).
					Updates(map[string]interface{}{"state": db.CashOrderPaymentSeen, "confirmations": confs}).Error
			}
		}
		amounts := nipCO.MintSplit(value, uint64(order.FeeMillis), terms.Kind.MaxMillis) //nolint:gosec // non-negative
		if value > 0 {
			credit := ledger.ExternalCredit{AppID: hub.ID, Amount: value, Purpose: "cash order", Key: p.PaymentKey,
				Description: "cash order payment"}
			if order.PaymentHash != nil && strings.HasPrefix(p.PaymentKey, "ln:") {
				credit.PaymentHash = *order.PaymentHash
				if order.Invoice != nil {
					credit.Invoice = *order.Invoice
				}
			} else {
				sum := sha256.Sum256([]byte("lokihub-order-payment:" + p.PaymentKey))
				credit.PaymentHash = hex.EncodeToString(sum[:])
			}
			if _, err := ledger.ExternalCreditTx(tx, credit); err != nil {
				return err
			}
		}
		now := e.now().Unix()
		if len(amounts) == 0 {
			logger.Logger.Warn().Uint("order_id", order.ID).Int64("value_millis", p.ValueMillis).
				Msg("An order payment of at most its fee mints nothing; the Hub keeps it")
			if err := tx.Model(&db.CashOrderPayment{}).Where("id = ?", p.ID).
				Updates(map[string]interface{}{"state": db.CashOrderPaymentKept, "minted_at": now}).Error; err != nil {
				return err
			}
		} else {
			if len(amounts) > 100 {
				logger.Logger.Warn().Uint("hub_app_id", hub.ID).Int("bills", len(amounts)).Msg("cash_order_mint_bills: one payment mints more than 100 bills")
			}
			claim := db.CashWalletClaim{IdentityType: order.RecipientType, IdentityValue: order.RecipientValue,
				MinTransferMloki: int64(terms.Bill.MinTransferMillis), RedeemFeePpm: int(terms.Bill.RedeemFeePpm), //nolint:gosec // validated
				RedeemFeeBaseMloki: int64(terms.Bill.RedeemFeeBase)} //nolint:gosec // validated
			if order.RecipientIA != nil {
				claim.IAPubkey = *order.RecipientIA
			}
			// An on-chain payment's bills are refused for cash_withdraw until it
			// has final_depth confirmations (NIP-CASH §Redeeming On-chain).
			if strings.HasPrefix(p.PaymentKey, "oc:") {
				ids := fmt.Sprintf("[%d]", p.ID)
				claim.UnfinalPaymentIDs = &ids
			}
			bills := make([]cashwallet.OrderedBill, len(amounts))
			for i, a := range amounts {
				bills[i] = cashwallet.OrderedBill{Amount: a, Claim: claim}
			}
			var err error
			minted, err = cashwallet.MintOrderedTx(ctx, tx, deps, &hub, bills)
			if err != nil {
				return err
			}
			var rec db.CashRequestRecord
			if err := tx.Where("request_id = ?", order.RequestID).First(&rec).Error; err != nil {
				return fmt.Errorf("the order's record: %w", err)
			}
			var ordinal int64
			if err := tx.Model(&db.CashRequestRecordBill{}).Where("record_id = ?", rec.ID).Count(&ordinal).Error; err != nil {
				return err
			}
			paid := p.ValueMillis
			var outpoint *string
			if strings.HasPrefix(p.PaymentKey, "oc:") {
				op := strings.TrimPrefix(p.PaymentKey, "oc:")
				outpoint = &op
			}
			for i := range minted {
				rb := minted[i].Record
				rb.RecordID = rec.ID
				rb.Ordinal = int16(ordinal) + int16(i) //nolint:gosec // bounded by the split floor
				rb.PaidMillis, rb.Outpoint = &paid, outpoint
				if err := tx.Create(&rb).Error; err != nil {
					return fmt.Errorf("record an ordered bill: %w", err)
				}
			}
			if err := tx.Model(&db.CashOrderPayment{}).Where("id = ?", p.ID).
				Updates(map[string]interface{}{"state": db.CashOrderPaymentMinted, "minted_at": now, "last_error": nil}).Error; err != nil {
				return err
			}
		}
		o, err := e.restateTx(tx, order.ID)
		if err != nil {
			return err
		}
		changed = o
		if changed == nil && len(minted) > 0 {
			changed = &order // a new bill on a closed order is also announced
		}
		return nil
	})
	if err != nil {
		minted = nil
		return e.mintFailed(paymentID, err)
	}
	for _, b := range minted {
		e.Apps.NotifyAppCreated(b.App)
	}
	if len(minted) > 0 {
		logger.Logger.Info().Uint("payment_id", paymentID).Int("bills", len(minted)).Msg("Minted an order payment")
	}
	e.notify(changed)
	return nil
}

// mintFailed keeps a payment confirmed and schedules its retry with backoff.
func (e *Engine) mintFailed(paymentID uint, cause error) error {
	var p db.CashOrderPayment
	if err := e.DB.First(&p, paymentID).Error; err != nil {
		return errors.Join(cause, err)
	}
	backoff := mintRetryMin << min(p.MintAttempts, 5)
	backoff = min(backoff, mintRetryMax)
	msg := cause.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if err := e.DB.Model(&db.CashOrderPayment{}).Where("id = ?", paymentID).Updates(map[string]interface{}{
		"mint_attempts": p.MintAttempts + 1, "next_attempt_at": e.now().Add(backoff).Unix(), "last_error": msg,
	}).Error; err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// RetryMints mints every confirmed payment whose retry is due (the 30-s job).
func (e *Engine) RetryMints(ctx context.Context) {
	var ids []uint
	if err := e.DB.Model(&db.CashOrderPayment{}).
		Where("state = ? AND order_id IS NOT NULL AND next_attempt_at <= ?", db.CashOrderPaymentConfirmed, e.now().Unix()).
		Pluck("id", &ids).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to list order payments to mint")
		return
	}
	for _, id := range ids {
		if err := e.MintPayment(ctx, id); err != nil {
			logger.Logger.Error().Err(err).Uint("payment_id", id).Msg("cash_order_mint_failures_total: an order payment could not be minted yet")
		}
	}
	var stale int64
	_ = e.DB.Model(&db.CashOrderPayment{}).Where("state = ? AND seen_at < ?", db.CashOrderPaymentConfirmed, e.now().Add(-10*time.Minute).Unix()).
		Count(&stale).Error
	if stale > 0 {
		logger.Logger.Warn().Int64("payments", stale).Msg("cash_order_payments_unminted: order payments wait more than 10 minutes to be minted")
	}
}

// SweepLightning re-reads every open Lightning order's invoice (at startup), so
// a settlement missed while the Hub was down is taken in.
func (e *Engine) SweepLightning(ctx context.Context) {
	ln := e.LN()
	if ln == nil {
		return
	}
	var orders []db.CashOrder
	if err := e.DB.Where("payment = ? AND state IN ?", nipCO.PaymentLightning, []string{db.CashOrderOpen, db.CashOrderPaid}).
		Find(&orders).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to list open Lightning orders")
		return
	}
	for _, o := range orders {
		if o.PaymentHash == nil {
			continue
		}
		inv, err := ln.LookupInvoice(ctx, *o.PaymentHash)
		if err != nil || inv == nil {
			continue
		}
		e.IntakeLightning(ctx, inv)
	}
}

// --- pay_by ---

// RunPayBy ends the payment window of every order whose pay_by passed (NIP-CO
// §Order State): an unpaid Lightning order's invoice is cancelled first and
// the order expires only once the node shows it cancelled with nothing settled
// or accepted; an on-chain order is closed when a payment was seen, else it
// expires. An on-chain window is ended only after a good chain scan: while the
// wallet cannot be read, those orders stay open and are retried next run.
func (e *Engine) RunPayBy(ctx context.Context) {
	now := e.now().Unix()
	var orders []db.CashOrder
	if err := e.DB.Where("pay_by < ? AND state IN ?", now, []string{db.CashOrderOpen, db.CashOrderPaid, db.CashOrderMinted}).
		Find(&orders).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to list orders past pay_by")
		return
	}
	scanned := true
	if hasOnchain(orders) {
		// An output already in the mempool at pay_by keeps its order: scan
		// before ending any on-chain window.
		e.notePayByHeight(ctx, orders)
		if err := e.ScanChain(ctx); err != nil {
			scanned = false
			e.logHeldOpen(err, orders, now)
		}
	}
	ln := e.LN()
	for _, o := range orders {
		if o.Payment == nipCO.PaymentOnchain {
			if !scanned {
				continue
			}
			if err := e.DB.First(&o, o.ID).Error; err != nil {
				continue
			}
		}
		e.endWindow(ctx, ln, o)
	}
}

func hasOnchain(orders []db.CashOrder) bool {
	for _, o := range orders {
		if o.Payment == nipCO.PaymentOnchain {
			return true
		}
	}
	return false
}

// payByHeightSlack bounds how long after pay_by the tip may be read for
// PayByHeight: a later read could count blocks mined after pay_by as in time.
const payByHeightSlack = 60

// notePayByHeight records the chain tip on each on-chain order just past
// pay_by that has none yet, read before the scan that ends its window. Best
// effort: with no tip read, an output the node reports no first-seen time for
// is judged by the time it is found.
func (e *Engine) notePayByHeight(ctx context.Context, orders []db.CashOrder) {
	now := e.now().Unix()
	var ids []uint
	for _, o := range orders {
		if o.Payment == nipCO.PaymentOnchain && o.State == db.CashOrderOpen && o.PayByHeight == nil && now-o.PayBy <= payByHeightSlack {
			ids = append(ids, o.ID)
		}
	}
	ln := e.LN()
	if len(ids) == 0 || ln == nil {
		return
	}
	info, err := ln.GetInfo(ctx)
	if err != nil || info == nil || info.BlockHeight == 0 {
		return
	}
	if err := e.DB.Model(&db.CashOrder{}).Where("id IN ? AND pay_by_height IS NULL", ids).
		Update("pay_by_height", int64(info.BlockHeight)).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to record the chain tip at pay_by")
	}
}

// logHeldOpen reports the on-chain orders a failed scan keeps open past
// pay_by. They are never expired on missing data, so this stays an error
// until the wallet can be read again.
func (e *Engine) logHeldOpen(err error, orders []db.CashOrder, now int64) {
	count, oldest := 0, now
	for _, o := range orders {
		if o.Payment == nipCO.PaymentOnchain {
			count++
			oldest = min(oldest, o.PayBy)
		}
	}
	logger.Logger.Error().Err(err).Int("orders", count).Int64("oldest_past_pay_by_seconds", now-oldest).
		Msg("cash_order_scan_failed_total: on-chain orders past pay_by stay open until the chain can be scanned")
}

// EndWindowIfDue is RunPayBy for one order, for a read of it: an order past
// its pay_by is read in the state NIP-CO gives it then (a Lightning order
// unpaid at pay_by is expired), not in the one the last job run left. An
// on-chain order is left open when the chain cannot be scanned.
func (e *Engine) EndWindowIfDue(ctx context.Context, orderID uint) {
	var o db.CashOrder
	now := e.now().Unix()
	if err := e.DB.Where("id = ? AND pay_by < ? AND state IN ?", orderID, now,
		[]string{db.CashOrderOpen, db.CashOrderPaid, db.CashOrderMinted}).Limit(1).Find(&o).Error; err != nil || o.ID == 0 {
		return
	}
	if o.Payment == nipCO.PaymentOnchain {
		e.notePayByHeight(ctx, []db.CashOrder{o})
		if err := e.ScanChain(ctx); err != nil {
			e.logHeldOpen(err, []db.CashOrder{o}, now)
			return
		}
		if err := e.DB.First(&o, o.ID).Error; err != nil {
			return
		}
	}
	e.endWindow(ctx, e.LN(), o)
}

// endWindow ends o's payment window, its pay_by passed.
func (e *Engine) endWindow(ctx context.Context, ln lnclient.LNClient, o db.CashOrder) {
	if o.Payment == nipCO.PaymentLightning && o.State == db.CashOrderOpen && o.PaymentHash != nil {
		if ln == nil {
			return
		}
		cancelErr := ln.CancelHoldInvoice(ctx, *o.PaymentHash)
		inv, err := ln.LookupInvoice(ctx, *o.PaymentHash)
		if err == nil && inv != nil && inv.SettledAt != nil {
			e.IntakeLightning(ctx, inv)
			return
		}
		state := ""
		if err == nil && inv != nil && inv.Metadata != nil {
			state, _ = inv.Metadata[lnclient.InvoiceStateKey].(string)
		}
		if state == "ACCEPTED" || state == "SETTLED" || (cancelErr != nil && state != "CANCELED") {
			return // left for the next run
		}
	}
	e.restate(o.ID)
}

func (e *Engine) restate(orderID uint) {
	var changed *db.CashOrder
	err := e.DB.Transaction(func(tx *gorm.DB) error {
		var o db.CashOrder
		if err := tx.First(&o, orderID).Error; err != nil {
			return err
		}
		if err := db.LockNamespace(tx, db.LockNsOrder, int32(o.HubAppID)); err != nil { //nolint:gosec // app ids are small
			return err
		}
		var err error
		changed, err = e.restateTx(tx, orderID)
		return err
	})
	if err != nil {
		logger.Logger.Error().Err(err).Uint("order_id", orderID).Msg("Failed to state an order")
		return
	}
	e.notify(changed)
}

// ErrOrderNotPayable: a same-node payment of an order's invoice reached an
// order that is no longer open or is past its pay_by; nothing is debited and
// the payment fails.
var ErrOrderNotPayable = errors.New("this order no longer takes payment")

// SelfPayLightning takes in a payment of an order's invoice by a wallet on this
// node, which the node cannot route to itself (NIP-CO §Lightning; NIP-CASH §The
// Redeem Fee): under the order lock the order must be open and within pay_by;
// the payment is recorded with the payer's amount, the invoice is cancelled at
// the node so it cannot be paid again, and the payment is minted. handled is
// false when the hash names no order.
func (e *Engine) SelfPayLightning(ctx context.Context, paymentHash string, amountMloki uint64) (preimage string, handled bool, err error) {
	var order db.CashOrder
	if err := e.DB.Where("payment_hash = ? AND payment = ?", paymentHash, nipCO.PaymentLightning).Limit(1).Find(&order).Error; err != nil {
		return "", false, err
	}
	if order.ID == 0 {
		return "", false, nil
	}
	ln := e.LN()
	if ln == nil {
		return "", true, errors.New("the node is not started")
	}
	inv, err := ln.LookupInvoice(ctx, paymentHash)
	if err != nil || inv == nil || inv.Preimage == "" {
		return "", true, fmt.Errorf("could not read the order invoice: %w", err)
	}
	if order.State != db.CashOrderOpen || e.now().Unix() > order.PayBy {
		return "", true, ErrOrderNotPayable
	}
	// The node invoice is cancelled before the payment is recorded: an external
	// payment can then no longer settle it as well, which would be neither
	// minted nor credited. One that already did makes the cancel fail, and this
	// payer is refused.
	if err := ln.CancelHoldInvoice(ctx, paymentHash); err != nil {
		logger.Logger.Warn().Err(err).Uint("order_id", order.ID).
			Msg("Could not cancel an order invoice before taking a payment of it on this node; refusing that payment")
		return "", true, ErrOrderNotPayable
	}
	var changed *db.CashOrder
	err = e.DB.Transaction(func(tx *gorm.DB) error {
		if err := db.LockNamespace(tx, db.LockNsOrder, int32(order.HubAppID)); err != nil { //nolint:gosec // app ids are small
			return err
		}
		if err := tx.First(&order, order.ID).Error; err != nil {
			return err
		}
		if order.State != db.CashOrderOpen || e.now().Unix() > order.PayBy {
			return ErrOrderNotPayable
		}
		hub := order.HubAppID
		p := db.CashOrderPayment{OrderID: &order.ID, HubAppID: &hub, PaymentKey: lightningKey(paymentHash),
			ValueMillis: int64(amountMloki), SeenAt: e.now().Unix(), Confirmations: 1, State: db.CashOrderPaymentConfirmed} //nolint:gosec // an invoice amount
		if err := tx.Create(&p).Error; err != nil {
			if db.IsDuplicateKeyError(err) {
				return ErrOrderNotPayable
			}
			return err
		}
		o, err := e.restateTx(tx, order.ID)
		changed = o
		return err
	})
	if err != nil {
		return "", true, err
	}
	e.notify(changed)
	e.mintPending(ctx, order.ID)
	return inv.Preimage, true, nil
}
