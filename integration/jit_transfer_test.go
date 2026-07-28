//go:build integration

// jit_transfer_test.go covers reassigning an unclaimed slice's registered
// identity without redeeming it (NIP-JW §Transferring a Slice), end to end
// over a real Nostr relay against a real running instance — the black-box
// counterpart to nip47/controllers/jit_transfer_controller_test.go's unit
// coverage.
package integration

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

func TestJITTransfer(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralJITHub(t, cfg, "jit-transfer-jit-hub", nil)
	testJITTransfer(t, cfg, hub)
}

func testJITTransfer(t *testing.T, cfg *Config, hub JITHubConfig) {
	hubClient := mustConnect(t, hub.Connection)

	t.Run("PubkeyToPubkey_ThenRedeemableByNewIdentity", func(t *testing.T) {
		currentPriv := newTestPrivkey(t)
		currentPub, err := nostr.GetPublicKey(currentPriv)
		require.NoError(t, err)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(currentPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		newPriv := newTestPrivkey(t)
		newPub, err := nostr.GetPublicKey(newPriv)
		require.NoError(t, err)

		proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "pubkey", newPub, nil, time.Now())
		var transferResult JITTransferResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: currentPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   JITTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		}, &transferResult))
		require.Equal(t, "pubkey", transferResult.IdentityType)
		require.Equal(t, newPub, transferResult.IdentityValue)

		// The OLD identity must no longer be able to redeem.
		oldInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration transfer old identity")
		oldProof := buildClaimProofEvent(t, currentPriv, created.WalletPubkey, oldInvoice.PaymentHash, nil, time.Now())
		var oldClaimResult ClaimFundsResult
		err = shared.Call(ctxT(t), constants.NIP47MethodJITRedeem, ClaimFundsParams{
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
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodJITRedeem, ClaimFundsParams{
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

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(currentPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "bearer", "", nil, time.Now())
		var transferResult JITTransferResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: currentPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   JITTransferNewIdentityParam{IdentityType: "bearer"},
		}, &transferResult))
		require.Equal(t, "bearer", transferResult.IdentityType)
		require.NotEmpty(t, transferResult.BearerSecret)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration transfer to bearer")
		var claimResult ClaimFundsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodJITRedeem, ClaimFundsParams{
			Invoice:      invoice.Invoice,
			BearerSecret: transferResult.BearerSecret,
		}, &claimResult))
		require.NotEmpty(t, claimResult.Preimage)
	})

	t.Run("ProofBoundToDifferentNewIdentity_Rejected", func(t *testing.T) {
		currentPriv := newTestPrivkey(t)
		currentPub, err := nostr.GetPublicKey(currentPriv)
		require.NoError(t, err)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(currentPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		intendedPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		// Proof is bound to intendedPub...
		proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "pubkey", intendedPub, nil, time.Now())

		// ...but the request targets a different (attacker) pubkey.
		attackerPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		var transferResult JITTransferResult
		err = shared.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: currentPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   JITTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: attackerPub},
		}, &transferResult)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})
}
