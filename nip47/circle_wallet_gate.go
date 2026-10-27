package nip47

import (
	"fmt"
	"time"

	"github.com/ohstr/nmilat/nipcw"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
)

// circleActiveMethods are every method a Circle Wallet connection is granted
// (NIP-CW §What a Circle Wallet Connection Allows), all served while Active.
var circleActiveMethods = map[string]bool{
	models.PAY_INVOICE_METHOD:               true,
	models.PAY_KEYSEND_METHOD:               true,
	models.MAKE_INVOICE_METHOD:              true,
	models.LOOKUP_INVOICE_METHOD:            true,
	models.LIST_TRANSACTIONS_METHOD:         true,
	models.GET_BALANCE_METHOD:               true,
	models.GET_BUDGET_METHOD:                true,
	models.GET_INFO_METHOD:                  true,
	constants.NIP47MethodDeleteCircleWallet: true,
}

// circlePayoutMethods are what a Suspended, Expired or Closing wallet serves:
// it can only pay out, read and delete itself.
var circlePayoutMethods = map[string]bool{
	models.GET_INFO_METHOD:                  true,
	models.GET_BALANCE_METHOD:               true,
	models.PAY_INVOICE_METHOD:               true,
	models.PAY_KEYSEND_METHOD:               true,
	models.LOOKUP_INVOICE_METHOD:            true,
	models.LIST_TRANSACTIONS_METHOD:         true,
	constants.NIP47MethodDeleteCircleWallet: true,
}

// circleWalletGate is the per-state method gate (NIP-CW §What a Circle Wallet
// Connection Allows): a method outside the wallet's list, or one its state does
// not serve, is RESTRICTED. delete_circle_wallet is served on a Circle Wallet's
// own connection only. ok is false with the error to answer with.
func circleWalletGate(app *db.App, method string, now time.Time) (ok bool, code, message string) {
	if app.Kind != db.AppKindCircleWallet {
		if method == constants.NIP47MethodDeleteCircleWallet {
			return false, constants.ERROR_RESTRICTED, "delete_circle_wallet is served on a circle wallet's own connection only"
		}
		return true, "", ""
	}
	// A frozen wallet's current key is refused everything (NIP-CW §Recovering
	// a Lost Connection), and such a request cancels nothing.
	if app.CircleFrozen {
		return false, nipcw.ErrFrozen, "this wallet is frozen: its current key is refused until it is unfrozen or rotated"
	}
	state := circlelife.EffectiveState(app, now)
	allowed := circlePayoutMethods
	if state == circlelife.StateActive {
		allowed = circleActiveMethods
	}
	if !allowed[method] {
		if !circleActiveMethods[method] {
			return false, constants.ERROR_RESTRICTED, fmt.Sprintf("%s is not served on a circle wallet", method)
		}
		return false, constants.ERROR_RESTRICTED, fmt.Sprintf("%s is not served on a %s circle wallet", method, state)
	}
	return true, "", ""
}
