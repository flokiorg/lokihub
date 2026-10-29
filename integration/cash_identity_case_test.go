//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// NIP-CASH §Minting Cash and §Transferring and Splitting a Slice (CASH-32,
// CASH-24): a pubkey identity is 64 lowercase hex. The Hub refuses an uppercase
// one with BAD_REQUEST on mint_cash and on cash_transfer's new_identity, since no
// claim proof could ever match it; the same key in lowercase is served.
func TestIdentityCase_UppercaseIdentityRefusedEndToEnd(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "identity-case-cash-hub", nil)
	hubClient := mustConnect(t, hub.Connection)

	t.Run("mint_cash", func(t *testing.T) {
		pub := mustPubkey(t, newTestPrivkey(t))
		var created MintCashResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(strings.ToUpper(pub), happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)

		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(pub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created), "the same key in lowercase mints")
	})

	t.Run("cash_transfer new_identity", func(t *testing.T) {
		currentPriv := newTestPrivkey(t)
		currentPub := mustPubkey(t, currentPriv)
		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(currentPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnectBill(t, created.CashToken, currentPriv)

		newPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		transfer := func(to string) error {
			proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "pubkey", to, "", happyPathAmountMloki, nil, time.Now())
			var res CashTransferResult
			return shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
				IdentityType:  "pubkey",
				IdentityValue: currentPub,
				IdentityEvent: eventJSON(t, proof),
				NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: to},
			}, &res)
		}
		requireNWCErrorCode(t, transfer(strings.ToUpper(newPub)), constants.ERROR_BAD_REQUEST)
		require.NoError(t, transfer(newPub), "the slice stayed with its holder and moves to the lowercase key")
	})
}
