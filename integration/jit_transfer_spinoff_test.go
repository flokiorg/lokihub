//go:build integration

// jit_transfer_spinoff_test.go covers spinning a multi-recipient wallet's
// slice off into a brand-new, dedicated single-bearer jit_wallet (NIP-JW
// "Spinning a slice off into a dedicated wallet") end to end over a real
// Nostr relay against a real running instance — the black-box counterpart to
// nip47/controllers/jit_transfer_controller_test.go's
// TestHandleJITTransferEvent_TransferIntoBearer_ClaimedCotenant_SpinsOffToNewWallet.
//
// This is the scenario the original mixing check used to reject outright: a
// wallet with a co-tenant (here, one who has already redeemed their own
// slice and so still holds the shared connection) transferring the OTHER
// slice to bearer. Rather than reject, jit_transfer now moves that slice's
// value into a brand-new wallet whose connection is delivered nested-
// encrypted to the caller's own pubkey — so the co-tenant, despite still
// holding the shared connection this response itself travels over, gets
// nothing usable out of it.
package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/nip47/cipher"
)

func TestJITTransferSpinOff(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralJITHub(t, cfg, "jit-transfer-spinoff-jit-hub", nil)
	testJITTransferSpinOff(t, cfg, hub)
}

// nwcURIFromLokicash mirrors nip47/controllers/pairing.go's
// buildNWCPairingURI — a real recipient has only the decoded lokicash token,
// not a ready-made pairing_uri, for a spun-off wallet (see NIP-JW: only the
// token travels, nested-encrypted, inside the jit_transfer response).
func nwcURIFromLokicash(token lokicash.Token) string {
	var b strings.Builder
	b.WriteString("nostr+walletconnect://")
	b.WriteString(token.WalletPubkey)
	b.WriteString("?relay=")
	b.WriteString(strings.Join(token.RelayURLs, "&relay="))
	b.WriteString("&secret=")
	b.WriteString(token.Secret)
	return b.String()
}

func testJITTransferSpinOff(t *testing.T, cfg *Config, hub JITHubConfig) {
	hubClient := mustConnect(t, hub.Connection)

	t.Run("ClaimedCotenant_SpinsOffToNewWallet_RedeemableThere_NotDecryptableByCotenant", func(t *testing.T) {
		attackerPriv := newTestPrivkey(t)
		attackerPub, err := nostr.GetPublicKey(attackerPriv)
		require.NoError(t, err)
		victimPriv := newTestPrivkey(t)
		victimPub, err := nostr.GetPublicKey(victimPriv)
		require.NoError(t, err)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "pubkey", IdentityValue: attackerPub, AmountMloki: happyPathAmountMloki},
				{IdentityType: "pubkey", IdentityValue: victimPub, AmountMloki: happyPathAmountMloki},
			},
			Expiry: happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		// The attacker redeems their own slice first. It's now claimed, but
		// the attacker still holds this shared connection and can decrypt
		// every future request/response on it.
		attackerInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "spinoff attacker redeem")
		attackerProof := buildClaimProofEvent(t, attackerPriv, created.WalletPubkey, attackerInvoice.PaymentHash, nil, time.Now())
		var attackerClaim ClaimFundsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodJITRedeem, ClaimFundsParams{
			Invoice:       attackerInvoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: attackerPub,
			IdentityEvent: eventJSON(t, attackerProof),
		}, &attackerClaim))
		require.NotEmpty(t, attackerClaim.Preimage)

		// The victim now spins their still-unclaimed slice off into its own
		// dedicated wallet, handing it a caller-generated bearer commitment.
		newSecretHex, newSecretHash := bearerSecretAndHash(t)
		proof := buildTransferProofEvent(t, victimPriv, created.WalletPubkey, "bearer", newSecretHash, nil, time.Now())
		var transferResult JITTransferResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: victimPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   JITTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: newSecretHash},
		}, &transferResult))
		require.Equal(t, "bearer", transferResult.IdentityType)
		require.Equal(t, newSecretHash, transferResult.IdentityValue)
		require.Equal(t, uint64(happyPathAmountMloki), transferResult.AmountMloki)
		require.NotEmpty(t, transferResult.NewWalletPubkey)
		require.NotEmpty(t, transferResult.NewWalletToken)

		// The OLD identity must no longer be able to redeem or transfer —
		// its slice's value has moved.
		oldInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "spinoff old identity redeem")
		oldProof := buildClaimProofEvent(t, victimPriv, created.WalletPubkey, oldInvoice.PaymentHash, nil, time.Now())
		var oldClaimResult ClaimFundsResult
		err = shared.Call(ctxT(t), constants.NIP47MethodJITRedeem, ClaimFundsParams{
			Invoice:       oldInvoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: victimPub,
			IdentityEvent: eventJSON(t, oldProof),
		}, &oldClaimResult)
		requireNWCErrorCode(t, err, constants.ERROR_NOT_FOUND)

		// The attacker — despite holding this shared connection and seeing
		// this exact same plaintext NewWalletPubkey/NewWalletToken — cannot
		// decrypt the inner token with their own privkey.
		attackerCipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, transferResult.NewWalletPubkey, attackerPriv)
		require.NoError(t, err)
		_, err = attackerCipher.Decrypt(transferResult.NewWalletToken)
		require.Error(t, err, "a claimed co-tenant must not be able to decrypt the spun-off wallet's connection")

		// The victim decrypts it with their own privkey plus the plaintext
		// NewWalletPubkey the response handed them — exactly what a real
		// black-box recipient has, nothing more (no DB access, no lookup).
		victimCipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, transferResult.NewWalletPubkey, victimPriv)
		require.NoError(t, err)
		decrypted, err := victimCipher.Decrypt(transferResult.NewWalletToken)
		require.NoError(t, err, "the intended recipient must be able to decrypt the inner token")
		newWalletToken, err := lokicash.Decode(decrypted)
		require.NoError(t, err)
		require.Equal(t, transferResult.NewWalletPubkey, newWalletToken.WalletPubkey)

		// The decrypted token is a genuinely live, spendable connection: the
		// new wallet holds exactly the victim's slice amount, redeemable with
		// the bearer secret the victim generated locally.
		newWalletClient := mustConnect(t, nwcURIFromLokicash(newWalletToken))
		newWalletInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "spinoff new wallet redeem")
		var newWalletClaim ClaimFundsResult
		require.NoError(t, newWalletClient.Call(ctxT(t), constants.NIP47MethodJITRedeem, ClaimFundsParams{
			Invoice:      newWalletInvoice.Invoice,
			BearerSecret: newSecretHex,
		}, &newWalletClaim))
		require.NotEmpty(t, newWalletClaim.Preimage)

		// And exactly once: a second redeem attempt against the same secret
		// must fail now that it's spent.
		replayInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "spinoff new wallet double-redeem")
		var replayClaim ClaimFundsResult
		err = newWalletClient.Call(ctxT(t), constants.NIP47MethodJITRedeem, ClaimFundsParams{
			Invoice:      replayInvoice.Invoice,
			BearerSecret: newSecretHex,
		}, &replayClaim)
		requireNWCErrorCode(t, err, constants.ERROR_NOT_FOUND)
	})
}
