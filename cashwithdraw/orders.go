package cashwithdraw

import (
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// OrderFacts are the NIP-CO facts a withdrawal reads (WP-21, WP-22): the
// highest on-chain confirmation tier a Cash Hub's order terms use (final_depth
// is at least it), whether an output script is an order address (any state,
// burnt included: own_address), and whether a slice is ordered and made from an
// on-chain payment not yet at finalDepth (unfinal).
//
// TODO(WP-21/WP-22): the orders branch provides the real implementation (its
// cash_order_addresses, cash_order_payments and the slice's ordered mark and
// unfinal_payment_ids); until it lands, noOrders reads as no orders at all.
type OrderFacts interface {
	HighestOnchainTier(tx *gorm.DB, hubAppID uint) (int, error)
	IsOrderScript(tx *gorm.DB, script []byte) (bool, error)
	Ordered(tx *gorm.DB, claim *db.CashWalletClaim) (ordered bool, unfinalPaymentIDs *string, err error)
	OrderedUnfinal(tx *gorm.DB, claim *db.CashWalletClaim, finalDepth int) (bool, error)
}

type noOrders struct{}

func (noOrders) HighestOnchainTier(*gorm.DB, uint) (int, error) { return 0, nil }
func (noOrders) IsOrderScript(*gorm.DB, []byte) (bool, error)   { return false, nil }
func (noOrders) Ordered(*gorm.DB, *db.CashWalletClaim) (bool, *string, error) {
	return false, nil, nil
}
func (noOrders) OrderedUnfinal(*gorm.DB, *db.CashWalletClaim, int) (bool, error) { return false, nil }
