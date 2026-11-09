package cashorder

import (
	"testing"

	"github.com/ohstr/nmilat/nipCO"
	"github.com/stretchr/testify/assert"

	"github.com/flokiorg/lokihub/db"
)

// WindowNotEnded reports an order past its pay_by whose window a good run has not
// ended, so its state is not known yet. Open always qualifies; paid/minted only
// for an on-chain order, since a good scan would have moved it to closed or
// expired — a Lightning order stays legitimately paid/minted past pay_by while
// the Hub retries minting (NIP-CO §Order State).
func TestWindowNotEnded(t *testing.T) {
	const payBy = 1_000
	for _, c := range []struct {
		name    string
		payment string
		state   string
		now     int64
		want    bool
	}{
		{"open before pay_by", nipCO.PaymentOnchain, db.CashOrderOpen, payBy, false},
		{"paid on-chain before pay_by", nipCO.PaymentOnchain, db.CashOrderPaid, payBy, false},

		{"open on-chain past pay_by", nipCO.PaymentOnchain, db.CashOrderOpen, payBy + 1, true},
		{"open Lightning past pay_by", nipCO.PaymentLightning, db.CashOrderOpen, payBy + 1, true},
		{"paid on-chain past pay_by", nipCO.PaymentOnchain, db.CashOrderPaid, payBy + 1, true},
		{"minted on-chain past pay_by", nipCO.PaymentOnchain, db.CashOrderMinted, payBy + 1, true},

		// A Lightning order is legitimately paid/minted past pay_by.
		{"paid Lightning past pay_by", nipCO.PaymentLightning, db.CashOrderPaid, payBy + 1, false},
		{"minted Lightning past pay_by", nipCO.PaymentLightning, db.CashOrderMinted, payBy + 1, false},

		// A window a good scan ended reports its real terminal state.
		{"closed on-chain past pay_by", nipCO.PaymentOnchain, db.CashOrderClosed, payBy + 1, false},
		{"expired on-chain past pay_by", nipCO.PaymentOnchain, db.CashOrderExpired, payBy + 1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := &db.CashOrder{Payment: c.payment, State: c.state, PayBy: payBy}
			assert.Equal(t, c.want, WindowNotEnded(o, c.now))
		})
	}
}
