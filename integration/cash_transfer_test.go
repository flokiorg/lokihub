//go:build integration

// cash_transfer_test.go covers reassigning an unclaimed slice's registered
// identity without redeeming it (NIP-JW §Transferring a Slice), end to end
// over a real Nostr relay against a real running instance — the black-box
// counterpart to nip47/controllers/cash_transfer_controller_test.go's unit
// coverage.
package integration

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

func TestCashTransfer(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "cash-transfer-cash-hub", nil)
	testCashTransfer(t, cfg, hub)
}

func testCashTransfer(t *testing.T, cfg *Config, hub CashHubConfig) {
	hubClient := mustConnect(t, hub.Connection)

	t.Run("PubkeyToPubkey_ThenRedeemableByNewIdentity", func(t *testing.T) {
		currentPriv := newTestPrivkey(t)
		currentPub, err := nostr.GetPublicKey(currentPriv)
		require.NoError(t, err)

		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(currentPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		newPriv := newTestPrivkey(t)
		newPub, err := nostr.GetPublicKey(newPriv)
		require.NoError(t, err)

		proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "pubkey", newPub, "", happyPathAmountMloki, nil, time.Now())
		var transferResult CashTransferResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: currentPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		}, &transferResult))
		require.Equal(t, "pubkey", transferResult.IdentityType)
		require.Equal(t, newPub, transferResult.IdentityValue)

		// The OLD identity must no longer be able to redeem.
		oldInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration transfer old identity")
		oldProof := buildClaimProofEvent(t, currentPriv, created.WalletPubkey, oldInvoice.PaymentHash, nil, time.Now())
		var oldClaimResult ClaimFundsResult
		err = shared.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice:       oldInvoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: currentPub,
			IdentityEvent: eventJSON(t, oldProof),
		}, &oldClaimResult)
		requireNWCErrorCode(t, err, constants.ERROR_NOT_FOUND)

		// The NEW identity must be able to redeem the full, unchanged amount.
		newInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration transfer new identity")
		newProof := buildClaimProofEvent(t, newPriv, created.WalletPubkey, newInvoice.PaymentHash, nil, time.Now())
		var newClaimResult ClaimFundsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice:       newInvoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: newPub,
			IdentityEvent: eventJSON(t, newProof),
		}, &newClaimResult))
		require.NotEmpty(t, newClaimResult.Preimage)
	})

	t.Run("PubkeyToBearer_ThenRedeemableWithSecret", func(t *testing.T) {
		currentPriv := newTestPrivkey(t)
		currentPub, err := nostr.GetPublicKey(currentPriv)
		require.NoError(t, err)

		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(currentPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		// The caller generates their own bearer secret and submits only its
		// commitment — the wallet never mints or returns one over this
		// shared connection (NIP-JW §Bearer Slices).
		newSecretHex, newSecretHash := bearerSecretAndHash(t)
		proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "bearer", newSecretHash, "", happyPathAmountMloki, nil, time.Now())
		var transferResult CashTransferResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: currentPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: newSecretHash},
		}, &transferResult))
		require.Equal(t, "bearer", transferResult.IdentityType)
		require.Equal(t, newSecretHash, transferResult.IdentityValue)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration transfer to bearer")
		var claimResult ClaimFundsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice:      invoice.Invoice,
			BearerSecret: newSecretHex,
		}, &claimResult))
		require.NotEmpty(t, claimResult.Preimage)
	})

	t.Run("ProofBoundToDifferentNewIdentity_Rejected", func(t *testing.T) {
		currentPriv := newTestPrivkey(t)
		currentPub, err := nostr.GetPublicKey(currentPriv)
		require.NoError(t, err)

		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(currentPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		intendedPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		// Proof is bound to intendedPub...
		proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "pubkey", intendedPub, "", happyPathAmountMloki, nil, time.Now())

		// ...but the request targets a different (attacker) pubkey.
		attackerPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		var transferResult CashTransferResult
		err = shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: currentPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: attackerPub},
		}, &transferResult)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})
}
