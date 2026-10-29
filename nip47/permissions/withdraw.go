package permissions

import (
	"slices"
	"sync/atomic"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// withdrawOffered reports whether a bill's Cash Hub offers on-chain withdrawals
// now; set once the withdrawal service exists. Unset: never.
var withdrawOffered atomic.Pointer[func(app *db.App) bool]

// SetWithdrawOffered sets the check behind private_methods' withdrawal entries.
func SetWithdrawOffered(f func(app *db.App) bool) { withdrawOffered.Store(&f) }

// withdrawPrivateMethods are cash_withdraw_quote and cash_withdraw, granted by
// the cash_redeem scope and listed in private_methods only while the bill's Cash
// Hub offers them (NIP-CASH §Scope Surface); never in methods.
func withdrawPrivateMethods(app *db.App, scopes []string) []string {
	f := withdrawOffered.Load()
	if f == nil || !slices.Contains(scopes, constants.CASH_REDEEM_SCOPE) || !(*f)(app) {
		return nil
	}
	return []string{constants.NIP47MethodCashWithdraw, constants.NIP47MethodCashWithdrawQuote}
}
