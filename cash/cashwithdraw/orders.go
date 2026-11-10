package cashwithdraw

import (
	"encoding/json"
	"fmt"

	"github.com/flokiorg/go-flokicoin/chaincfg"
	"github.com/flokiorg/go-flokicoin/txscript"
	"github.com/ohstr/nmilat/nipCO"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// OrderFacts are the NIP-CO facts a withdrawal reads: the highest on-chain
// confirmation tier a Cash Hub's order terms use (final_depth is at least it),
// whether an output script is an order address (any state, burnt included:
// own_address), and whether a slice is ordered and made from an on-chain
// payment not yet at finalDepth (unfinal).
type OrderFacts interface {
	HighestOnchainTier(tx *gorm.DB, hubAppID uint) (int, error)
	IsOrderScript(tx *gorm.DB, script []byte) (bool, error)
	Ordered(tx *gorm.DB, claim *db.CashWalletClaim) (ordered bool, unfinalPaymentIDs *string, err error)
	OrderedUnfinal(tx *gorm.DB, claim *db.CashWalletClaim, finalDepth int) (bool, error)
}

// DBOrders reads the facts from the order tables: cash_order_terms and the
// terms frozen on each order still being paid, cash_order_addresses, the
// slice's ordered and unfinal_payment_ids columns and cash_order_payments.
type DBOrders struct{}

// HighestOnchainTier is the highest n of the hub's current on-chain tiers and of
// those frozen on its on-chain orders not yet minted, expired or closed.
func (DBOrders) HighestOnchainTier(tx *gorm.DB, hubAppID uint) (int, error) {
	highest := 0
	tiers := func(k *nipCO.KindTerms) {
		if k == nil {
			return
		}
		for _, t := range k.Confirmations {
			highest = max(highest, int(t.N))
		}
	}
	var row db.CashOrderTerms
	if err := tx.Where("hub_app_id = ?", hubAppID).Limit(1).Find(&row).Error; err != nil {
		return 0, err
	}
	if row.HubAppID != 0 {
		var terms nipCO.Terms
		if err := json.Unmarshal([]byte(row.TermsJSON), &terms); err != nil {
			return 0, fmt.Errorf("read hub %d's order terms: %w", hubAppID, err)
		}
		tiers(terms.Onchain)
	}
	var frozen []string
	if err := tx.Model(&db.CashOrder{}).Where("hub_app_id = ? AND payment = ? AND state IN ?", hubAppID,
		nipCO.PaymentOnchain, []string{db.CashOrderOpen, db.CashOrderPaid}).Pluck("terms_json", &frozen).Error; err != nil {
		return 0, err
	}
	for _, raw := range frozen {
		var t struct {
			Kind nipCO.KindTerms `json:"kind"`
		}
		if json.Unmarshal([]byte(raw), &t) == nil {
			tiers(&t.Kind)
		}
	}
	return highest, nil
}

// scriptNets are the networks an output script is spelled as an address in;
// order addresses are stored as the node spelled them.
var scriptNets = []*chaincfg.Params{&chaincfg.MainNetParams, &chaincfg.TestNet3Params,
	&chaincfg.RegressionNetParams, &chaincfg.SimNetParams}

// IsOrderScript reports whether script pays an address the hub ever issued for
// an order, burnt included.
func (DBOrders) IsOrderScript(tx *gorm.DB, script []byte) (bool, error) {
	var addrs []string
	for _, p := range scriptNets {
		_, as, _, err := txscript.ExtractPkScriptAddrs(script, p)
		if err != nil {
			continue
		}
		for _, a := range as {
			addrs = append(addrs, a.EncodeAddress())
		}
	}
	if len(addrs) == 0 {
		return false, nil
	}
	var n int64
	if err := tx.Model(&db.CashOrderAddress{}).Where("address IN ?", addrs).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// Ordered is the slice's own ordered mark and unfinal_payment_ids.
func (DBOrders) Ordered(_ *gorm.DB, claim *db.CashWalletClaim) (bool, *string, error) {
	return claim.Ordered, claim.UnfinalPaymentIDs, nil
}

// OrderedUnfinal reports an ordered slice made from an on-chain payment that is
// not minted at finalDepth confirmations or more. A payment row already dropped
// by the order retention is long final.
func (DBOrders) OrderedUnfinal(tx *gorm.DB, claim *db.CashWalletClaim, finalDepth int) (bool, error) {
	if !claim.Ordered {
		return false, nil
	}
	ids, err := db.ParsePaymentIDs(claim.UnfinalPaymentIDs)
	if err != nil || len(ids) == 0 {
		return false, err
	}
	var n int64
	if err := tx.Model(&db.CashOrderPayment{}).Where("id IN ? AND NOT (state = ? AND confirmations >= ?)",
		ids, db.CashOrderPaymentMinted, finalDepth).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}
