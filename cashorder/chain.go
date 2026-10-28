package cashorder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/ohstr/nmilat/nipCO"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/ledger"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
)

// ScanChain is the on-chain intake (NIP-CO §On-chain), run every 30 s: every
// output to an order address is one payment, keyed by its outpoint. It is seen
// in the mempool or a block, confirmed once it has the confirmations of the
// tier its own value falls in (never fewer than one), then minted. An
// unconfirmed output missing from two scans in a row is dropped (minted
// only if it comes back); a minted one that leaves the chain is reorged, its
// bill standing at the Hub's loss. An output to an address with no live order
// (burnt, its order deleted unpaid, or first paid after pay_by with nothing
// seen) is kept by the Hub: credited to it as income once confirmed, or to no
// app when the Hub is gone.
func (e *Engine) ScanChain(ctx context.Context) {
	ln := e.LN()
	lister, ok := ln.(lnclient.OnchainOutputLister)
	if !ok || ln == nil {
		return
	}
	outputs, err := lister.ListOnchainOutputs(ctx)
	if err != nil {
		logger.Logger.Warn().Err(err).Msg("Could not list on-chain outputs for order intake")
		return
	}
	byAddress := map[string][]lnclient.OnchainOutput{}
	for _, o := range outputs {
		byAddress[o.Address] = append(byAddress[o.Address], o)
	}
	addrs := make([]string, 0, len(byAddress))
	for a := range byAddress {
		addrs = append(addrs, a)
	}
	var known []db.CashOrderAddress
	for start := 0; start < len(addrs); start += 500 {
		end := min(start+500, len(addrs))
		var part []db.CashOrderAddress
		if err := e.DB.Where("address IN ?", addrs[start:end]).Find(&part).Error; err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to read order addresses")
			return
		}
		known = append(known, part...)
	}
	present := map[string]bool{}
	for _, a := range known {
		for _, o := range byAddress[a.Address] {
			key := onchainKey(o.TxID, o.Vout)
			present[key] = true
			e.observeOutput(ctx, a, o, key)
		}
	}
	e.missingOutputs(present)

	var orderIDs []uint
	if err := e.DB.Model(&db.CashOrderPayment{}).Distinct().Where("state = ? AND order_id IS NOT NULL AND payment_key LIKE ?",
		db.CashOrderPaymentConfirmed, "oc:%").Pluck("order_id", &orderIDs).Error; err == nil {
		for _, id := range orderIDs {
			e.mintPending(ctx, id)
		}
	}
}

// observeOutput records or advances the payment of one output to an order
// address.
func (e *Engine) observeOutput(ctx context.Context, addr db.CashOrderAddress, o lnclient.OnchainOutput, key string) {
	value := o.AmountLoki * 1000
	if value <= 0 {
		return
	}
	var p db.CashOrderPayment
	if err := e.DB.Where("payment_key = ?", key).Limit(1).Find(&p).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to read an order payment")
		return
	}
	var order *db.CashOrder
	if p.ID == 0 {
		order = e.liveOrderFor(addr, o.SeenAt)
		hub := addr.HubAppID
		address := addr.Address
		p = db.CashOrderPayment{PaymentKey: key, HubAppID: &hub, Address: &address, ValueMillis: value,
			SeenAt: e.now().Unix(), Confirmations: int(o.Confirmations), State: db.CashOrderPaymentSeen}
		if order != nil {
			p.OrderID = &order.ID
		} else {
			logger.Logger.Warn().Uint("hub_app_id", addr.HubAppID).Int64("value_millis", value).
				Msg("cash_order_kept_payments_total: a payment to an order address with no live order is kept by the Hub")
		}
		if _, err := e.insertPayment(&p, order); err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to record an on-chain order payment")
			return
		}
		if err := e.DB.Where("payment_key = ?", key).First(&p).Error; err != nil {
			return
		}
	} else if p.OrderID != nil {
		var o db.CashOrder
		if err := e.DB.Limit(1).Find(&o, *p.OrderID).Error; err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to read an order payment's order")
			return
		}
		if o.ID != 0 {
			order = &o
		}
	}

	conf := int(o.Confirmations)
	updates := map[string]interface{}{"confirmations": conf, "missed_scans": 0}
	if p.OrderID != nil && order == nil {
		// Its order is gone: the payment is unbound, and kept by the Hub.
		updates["order_id"] = nil
		p.OrderID = nil
	}
	if o.BlockHeight > 0 {
		updates["block_height"] = int(o.BlockHeight)
	}
	switch p.State {
	case db.CashOrderPaymentDropped:
		updates["state"] = db.CashOrderPaymentSeen // seen again: back to seen, seen_at unchanged
		p.State = db.CashOrderPaymentSeen
	}
	if p.State == db.CashOrderPaymentSeen {
		if p.OrderID == nil {
			if conf >= 1 {
				if err := e.keepTx(p, conf); err != nil {
					logger.Logger.Error().Err(err).Msg("Failed to keep an unbound order payment")
				}
				return
			}
		} else if order != nil && conf >= requiredConfirmations(order, uint64(value)) { //nolint:gosec // positive
			updates["state"] = db.CashOrderPaymentConfirmed
		}
	}
	// A confirmed payment not minted yet that a reorganisation left below its
	// tier goes back to seen: it is never minted short of its confirmations.
	if p.State == db.CashOrderPaymentConfirmed && order != nil && conf < requiredConfirmations(order, uint64(value)) { //nolint:gosec // positive
		updates["state"] = db.CashOrderPaymentSeen
	}
	changed := false
	err := e.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&db.CashOrderPayment{}).Where("id = ?", p.ID).Updates(updates).Error; err != nil {
			return err
		}
		if p.OrderID != nil {
			o, err := e.restateTx(tx, *p.OrderID)
			if err != nil {
				return err
			}
			if o != nil {
				order, changed = o, true
			}
		}
		return nil
	})
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to advance an on-chain order payment")
		return
	}
	if changed {
		e.notify(order)
	}
	_ = ctx
}

// requiredConfirmations is the n of the tier value falls in under the order's
// frozen terms, at least 1 (no 0-conf).
func requiredConfirmations(order *db.CashOrder, value uint64) int {
	var terms orderTerms
	if err := json.Unmarshal([]byte(order.TermsJSON), &terms); err != nil {
		return 6
	}
	n, err := nipCO.ConfirmationsFor(terms.Kind.Confirmations, value)
	if err != nil || n < 1 {
		return max(int(n), 1)
	}
	return int(n)
}

// liveOrderFor is the order a new output to addr pays: the address's order,
// unless it is gone or expired (nothing seen by pay_by); nil means the Hub keeps
// the payment. seenAt is when the node's wallet first saw the output's
// transaction (0 unknown): an output in the mempool by pay_by pays its order
// even when the scan that finds it runs after pay_by.
func (e *Engine) liveOrderFor(addr db.CashOrderAddress, seenAt int64) *db.CashOrder {
	if addr.OrderID == nil || addr.Burnt {
		return nil
	}
	var o db.CashOrder
	if err := e.DB.Limit(1).Find(&o, *addr.OrderID).Error; err != nil || o.ID == 0 {
		return nil
	}
	// Nothing seen by pay_by: the order expired (whether or not the pay_by job
	// has stated it yet), and a first payment after it is kept.
	seen := e.now().Unix()
	if seenAt > 0 && seenAt < seen {
		seen = seenAt
	}
	if o.State == db.CashOrderExpired || (o.State == db.CashOrderOpen && seen > o.PayBy) {
		return nil
	}
	return &o
}

// keepTx credits a confirmed unbound payment to its Hub as income, or to no
// app when the Hub is gone (the coins stay in the node's wallet).
func (e *Engine) keepTx(p db.CashOrderPayment, conf int) error {
	return e.DB.Transaction(func(tx *gorm.DB) error {
		var hub db.App
		if p.HubAppID != nil {
			if err := tx.Limit(1).Find(&hub, *p.HubAppID).Error; err != nil {
				return err
			}
		}
		if hub.ID != 0 {
			if err := db.LockApp(tx, hub.ID); err != nil {
				return err
			}
			sum := sha256.Sum256([]byte("lokihub-order-payment:" + p.PaymentKey))
			if _, err := ledger.ExternalCreditTx(tx, ledger.ExternalCredit{AppID: hub.ID, Amount: uint64(p.ValueMillis), //nolint:gosec // positive
				PaymentHash: hex.EncodeToString(sum[:]), Purpose: "cash order kept", Key: p.PaymentKey,
				Description: "payment to an order address with no live order"}); err != nil {
				return err
			}
		} else {
			logger.Logger.Warn().Str("payment_key", p.PaymentKey).
				Msg("A payment to the address of an order deleted with its Cash Hub stays in the node's wallet")
		}
		now := e.now().Unix()
		return tx.Model(&db.CashOrderPayment{}).Where("id = ? AND state = ?", p.ID, db.CashOrderPaymentSeen).
			Updates(map[string]interface{}{"state": db.CashOrderPaymentKept, "confirmations": conf, "minted_at": now}).Error
	})
}

// missingOutputs handles on-chain payments the scan did not find: an
// unconfirmed one is dropped after droppedAfterMisses scans; a confirmed,
// unminted one lost its block and goes back to seen; a minted one is reorged.
func (e *Engine) missingOutputs(present map[string]bool) {
	var payments []db.CashOrderPayment
	if err := e.DB.Where("payment_key LIKE ? AND state IN ?", "oc:%",
		[]string{db.CashOrderPaymentSeen, db.CashOrderPaymentConfirmed, db.CashOrderPaymentMinted}).Find(&payments).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to list on-chain order payments")
		return
	}
	for _, p := range payments {
		if present[p.PaymentKey] {
			continue
		}
		updates := map[string]interface{}{"missed_scans": p.MissedScans + 1, "confirmations": 0}
		switch p.State {
		case db.CashOrderPaymentMinted:
			updates["state"] = db.CashOrderPaymentReorged
			logger.Logger.Error().Str("payment_key", p.PaymentKey).
				Msg("cash_order_reorg_after_mint_total: a minted order payment left the chain; its bill stands at the Hub's loss")
		case db.CashOrderPaymentConfirmed:
			updates["state"] = db.CashOrderPaymentSeen
		case db.CashOrderPaymentSeen:
			if p.MissedScans+1 >= droppedAfterMisses {
				updates["state"] = db.CashOrderPaymentDropped
			}
		}
		if err := e.DB.Model(&db.CashOrderPayment{}).Where("id = ?", p.ID).Updates(updates).Error; err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to update a missing order payment")
			continue
		}
		if p.OrderID != nil {
			e.restate(*p.OrderID)
		}
	}
}
