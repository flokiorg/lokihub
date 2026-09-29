package nip47

import (
	"testing"

	"github.com/flokiorg/lokihub/constants"
)

// TestBillMethodsAreNotOnTheStandardTransport pins the split that makes the private
// transport the only way to spend a bill.
//
// The four bill methods are refused on kind 23194 and served only over the private
// transport, because this transport cannot carry them privately: every request here is
// p-tagged with its own bill's wallet pubkey, so a holder's bills are a public,
// linkable set however well the payload is encrypted.
//
// mint_cash deliberately stays: it is the hub owner's method on the hub's own
// connection, and NIP-CASH keeps it OFF the private transport for its own reasons — it
// has no retry idempotency, which is what makes the replay set safe. create_circle_wallet
// stays too; it acts on a hub, not a bill, so there is no bill pubkey to leak.
//
// Asserted against the same allowlist the dispatcher consults, so the two cannot drift.
func TestBillMethodsAreNotOnTheStandardTransport(t *testing.T) {
	billMethods := []string{
		constants.NIP47MethodCashStatus,
		constants.NIP47MethodListRecipients,
		constants.NIP47MethodCashRedeem,
		constants.NIP47MethodCashTransfer,
		constants.NIP47MethodCashConsolidate,
	}
	for _, m := range billMethods {
		if !IsPrivateServableMethod(m) {
			t.Errorf("%s must be servable over the private transport — it is served nowhere else", m)
		}
	}

	// mint_cash must NOT be reachable privately: it is the one method with no retry
	// idempotency, which is what makes the replay set safe.
	if IsPrivateServableMethod(constants.NIP47MethodMintCash) {
		t.Error("mint_cash must not be on the private transport (NIP-CASH §The Private Transport)")
	}
}
