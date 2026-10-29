package cashorder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/ohstr/nmilat/nipCO"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/ledger"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/testknobs"
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
// app when the Hub is gone. An error means the wallet could not be read: no
// on-chain window may be ended on it.
//
// A scan reads the blocks from its stored floor up, and the mempool: the floor
// trails the tip by chainRescanDepth, so a payment deeper than that is no
// longer read from the node, and its confirmations are counted from its height.
func (e *Engine) ScanChain(ctx context.Context) error {
	ln := e.LN()
	lister, ok := ln.(lnclient.OnchainOutputLister)
	if !ok || ln == nil {
		return errNoChain
	}
	if testknobs.ChainScanFails() {
		return errors.New("list on-chain outputs: failed by a test fault")
	}
	tip, tipErr := chainTip(ctx, ln)
	from := e.scanFrom()
	outputs, err := lister.ListOnchainOutputs(ctx, from)
	if err != nil {
		logger.Logger.Warn().Err(err).Msg("Could not list on-chain outputs for order intake")
		return fmt.Errorf("list on-chain outputs: %w", err)
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
			return fmt.Errorf("read order addresses: %w", err)
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
	e.missingOutputs(present, from)
	if tipErr == nil {
		e.ageDeepPayments(tip, from)
		e.storeScanFloor(tip)
	}

	var orderIDs []uint
	if err := e.DB.Model(&db.CashOrderPayment{}).Distinct().Where("state = ? AND order_id IS NOT NULL AND payment_key LIKE ?",
		db.CashOrderPaymentConfirmed, "oc:%").Pluck("order_id", &orderIDs).Error; err == nil {
		for _, id := range orderIDs {
			e.mintPending(ctx, id)
		}
	}
	return nil
}

// errNoChain: the node is not started or cannot list its wallet's outputs.
var errNoChain = errors.New("no node wallet to scan")

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
		order = e.liveOrderFor(addr, o)
		hub := addr.HubAppID
		address := addr.Address
		p = db.CashOrderPayment{PaymentKey: key, HubAppID: &hub, Address: &address, ValueMillis: value,
			SeenAt: firstSeen(o.SeenAt, e.now().Unix()), Confirmations: int(o.Confirmations), State: db.CashOrderPaymentSeen}
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
// the payment. out.SeenAt is when the node's wallet first saw the output's
// transaction (0 unknown): an output in the mempool by pay_by pays its order
// even when the scan that finds it runs after pay_by. So does one in a block at
// or below the tip read when pay_by passed, whatever SeenAt says (a rescan
// reports 0 or the block time).
func (e *Engine) liveOrderFor(addr db.CashOrderAddress, out lnclient.OnchainOutput) *db.CashOrder {
	if addr.OrderID == nil || addr.Burnt {
		return nil
	}
	var o db.CashOrder
	if err := e.DB.Limit(1).Find(&o, *addr.OrderID).Error; err != nil || o.ID == 0 {
		return nil
	}
	// Nothing seen by pay_by: the order expired (whether or not the pay_by job
	// has stated it yet), and a first payment after it is kept.
	if o.State == db.CashOrderExpired {
		return nil
	}
	inBlockByPayBy := out.BlockHeight > 0 && o.PayByHeight != nil && int64(out.BlockHeight) <= *o.PayByHeight
	if o.State == db.CashOrderOpen && firstSeen(out.SeenAt, e.now().Unix()) > o.PayBy && !inBlockByPayBy {
		return nil
	}
	return &o
}

// firstSeen is the node's first-seen time of a transaction when it reports
// one earlier than now, else now.
func firstSeen(nodeSeenAt, now int64) int64 {
	if nodeSeenAt > 0 && nodeSeenAt < now {
		return nodeSeenAt
	}
	return now
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

// chainRescanDepth is how many blocks below the tip every scan reads again; a
// minted payment deeper than that is final for the intake.
const chainRescanDepth = 100

// scanFloorKey stores the lowest block height the next scan reads.
const scanFloorKey = "CashOrderScanFloor"

// chainTip is the node's best block height.
func chainTip(ctx context.Context, ln lnclient.LNClient) (int32, error) {
	info, err := ln.GetInfo(ctx)
	if err != nil {
		return 0, err
	}
	if info == nil || info.BlockHeight == 0 || info.BlockHeight > math.MaxInt32 {
		return 0, errors.New("the node reports no block height")
	}
	return int32(info.BlockHeight), nil
}

// scanFrom is where a scan starts: the stored floor (0, the whole history,
// before the first scan), lowered to the block of any payment seen or
// confirmed but not yet minted, which stays read until it is.
func (e *Engine) scanFrom() int32 {
	var row db.UserConfig
	from := int64(0)
	if err := e.DB.Where(&db.UserConfig{Key: scanFloorKey}).Limit(1).Find(&row).Error; err == nil && row.ID != 0 {
		if v, err := strconv.ParseInt(row.Value, 10, 32); err == nil && v > 0 {
			from = v
		}
	}
	var lowest *int
	if err := e.DB.Model(&db.CashOrderPayment{}).Where("payment_key LIKE ? AND state IN ? AND block_height > 0", "oc:%",
		[]string{db.CashOrderPaymentSeen, db.CashOrderPaymentConfirmed}).Select("MIN(block_height)").Scan(&lowest).Error; err != nil {
		return 0
	}
	if lowest != nil && int64(*lowest) < from {
		from = int64(*lowest)
	}
	return int32(from) //nolint:gosec // parsed as 32 bits
}

// storeScanFloor stores the floor the next scan starts from: chainRescanDepth
// blocks below the tip this scan read up to.
func (e *Engine) storeScanFloor(tip int32) {
	floor := strconv.Itoa(int(max(tip-chainRescanDepth+1, 0)))
	err := e.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&db.UserConfig{}).Where(&db.UserConfig{Key: scanFloorKey}).Update("value", floor)
		if res.Error != nil || res.RowsAffected > 0 {
			return res.Error
		}
		return tx.Create(&db.UserConfig{Key: scanFloorKey, Value: floor}).Error
	})
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to store the order intake's scan floor; the next scan reads more")
	}
}

// ageDeepPayments counts the confirmations of minted payments below the scan's
// floor from their height, as the scan no longer reads them.
func (e *Engine) ageDeepPayments(tip, from int32) {
	if err := e.DB.Model(&db.CashOrderPayment{}).
		Where("payment_key LIKE ? AND state = ? AND block_height > 0 AND block_height < ?", "oc:%", db.CashOrderPaymentMinted, from).
		Update("confirmations", gorm.Expr("? - block_height + 1", tip)).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to age deep order payments")
	}
}

// missingOutputs handles on-chain payments the scan did not find: an
// unconfirmed one is dropped after droppedAfterMisses scans; a confirmed,
// unminted one lost its block and goes back to seen; a minted one is reorged.
// A minted payment below from was not read, so it is not missing.
func (e *Engine) missingOutputs(present map[string]bool, from int32) {
	var payments []db.CashOrderPayment
	if err := e.DB.Where("payment_key LIKE ? AND state IN ?", "oc:%",
		[]string{db.CashOrderPaymentSeen, db.CashOrderPaymentConfirmed, db.CashOrderPaymentMinted}).
		Where("state <> ? OR block_height IS NULL OR block_height >= ?", db.CashOrderPaymentMinted, from).
		Find(&payments).Error; err != nil {
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
