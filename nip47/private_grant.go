package nip47

import (
	"time"

	"github.com/ohstr/nmilat/nipcash/transport"

	"github.com/flokiorg/lokihub/constants"
)

// A Pre-Authorisation grant (NIP-CASH §Pre-Authorisation) lets a bill holder
// delegate limited authority over a bill to another key, so a delegate can act on
// the bill WITHOUT holding the bearer connection secret. The grant is signed by the
// bill's connection secret — the same key a kind-23193 bill proof is signed with —
// so the hub recovers the same connection pubkey it already checks against the bill
// at I3, and accepts the grant IN PLACE OF a bill proof. The one load-bearing rule
// is enforced in privateItemIsAuthorized: the item's own slice proof must be signed
// by the grant's named delegate, or a leaked grant would be a second bearer secret.
//
// grantReadMethods are the methods a grant may authorise today. Both are read-only:
// the delegate reads a bill it does not own (an accountant polling cash_status, say).
// A value method under a grant (a pull payment) needs amount-cap and single-use
// enforcement — see servePrivateItem's grant gate — and is not yet served.
var grantReadMethods = map[string]bool{
	constants.NIP47MethodCashStatus:        true,
	constants.NIP47MethodCashWithdrawQuote: true,
}

// verifyGrant verifies an item's Pre-Authorisation grant against the bill it names.
//
// ok is false when the item carries no grant OR the grant does not verify — the two
// are deliberately indistinguishable to the caller, which answers either with
// silence, because a grant that fails is a possession failure and a hub never
// confirms possession it cannot prove (no oracle). The returned ConnKey is the bill
// connection key that signed the grant; the caller checks it holds the bill exactly
// as it checks a bill proof's signer.
func (svc *nip47Service) verifyGrant(item transport.Item, binding PrivateItemBinding) (transport.GrantInfo, bool) {
	if !item.HasGrant() {
		return transport.GrantInfo{}, false
	}
	info, err := transport.VerifyGrant(item.Grant, transport.GrantBinding{
		Target:   item.Target,
		HubXOnly: binding.HubXOnly,
		Method:   item.Method,
	}, time.Now())
	if err != nil {
		return transport.GrantInfo{}, false
	}
	return info, true
}
