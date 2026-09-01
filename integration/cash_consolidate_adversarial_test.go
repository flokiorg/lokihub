//go:build integration

// cash_consolidate_adversarial_test.go drives cash_consolidate and mint
// provenance against a real running instance as an attacker would: combining
// wallets across hubs, presenting proofs the caller doesn't own, re-spending an
// already-consolidated source, and restating a signed token's denomination.
// Each must be refused or detected, over real relay round-trips and real
// Lightning self-payments.
package integration

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/nip47/cipher"
)

// decryptSplitWalletToken decrypts a nested-encrypted split/consolidate token
// (delivered to the caller keyed to their own privkey) and returns the decoded
// token — for inspecting provenance without connecting.
func decryptSplitWalletToken(t *testing.T, walletPubkey, encToken, callerPriv string) lokicash.Token {
	t.Helper()
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, walletPubkey, callerPriv)
	require.NoError(t, err)
	dec, err := c.Decrypt(encToken)
	require.NoError(t, err)
	tok, err := lokicash.Decode(dec)
	require.NoError(t, err)
	require.Equal(t, walletPubkey, tok.WalletPubkey)
	return tok
}

// mintPubkeySource mints a single-pubkey cash_wallet under hubClient for
// ownerPub and returns its wallet pubkey + shared connection.
func mintPubkeySource(t *testing.T, hubClient *nwcclient.Client, ownerPub string, amount uint64) (walletPubkey, conn string) {
	t.Helper()
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(ownerPub, amount),
		Expiry:     happyPathExpirySecs,
	}, &created))
	return created.WalletPubkey, created.PairingURI
}

// consolidateSourceFor builds a source param for a wallet the caller owns, bound
// to newPub (proof amount = the source slice's full amount).
func consolidateSourceFor(t *testing.T, walletPubkey, proofSignerPriv, callerPub, newPub string, amount uint64) ConsolidateSourceParam {
	t.Helper()
	proof := buildTransferProofEvent(t, proofSignerPriv, walletPubkey, "pubkey", newPub, "", amount, nil, time.Now())
	return ConsolidateSourceParam{
		WalletPubkey:  walletPubkey,
		IdentityType:  "pubkey",
		IdentityValue: callerPub,
		IdentityEvent: eventJSON(t, proof),
	}
}

func TestConsolidate_Adversarial(t *testing.T) {
	cfg := requireConfig(t)
	hubA, _, _ := createEphemeralCashHub(t, cfg, "consolidate-adv-hubA", nil)
	hubB, _, _ := createEphemeralCashHub(t, cfg, "consolidate-adv-hubB", nil)
	clientA := mustConnect(t, hubA.Connection)
	clientB := mustConnect(t, hubB.Connection)

	callerPriv := newTestPrivkey(t)
	callerPub, err := nostr.GetPublicKey(callerPriv)
	require.NoError(t, err)
	newPriv := newTestPrivkey(t)
	newPub, err := nostr.GetPublicKey(newPriv)
	require.NoError(t, err)

	t.Run("CrossHub_Rejected", func(t *testing.T) {
		wpA, connA := mintPubkeySource(t, clientA, callerPub, happyPathAmountMloki)
		wpB, _ := mintPubkeySource(t, clientB, callerPub, happyPathAmountMloki)
		callConn := mustConnect(t, connA)
		var res CashConsolidateResult
		err := callConn.Call(ctxT(t), constants.NIP47MethodCashConsolidate, CashConsolidateParams{
			Sources: []ConsolidateSourceParam{
				consolidateSourceFor(t, wpA, callerPriv, callerPub, newPub, happyPathAmountMloki),
				consolidateSourceFor(t, wpB, callerPriv, callerPub, newPub, happyPathAmountMloki),
			},
			NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
		require.ErrorContains(t, err, "same Cash Hub")
	})

	t.Run("UncustodiedSource_Rejected", func(t *testing.T) {
		wpA, connA := mintPubkeySource(t, clientA, callerPub, happyPathAmountMloki)
		callConn := mustConnect(t, connA)
		bogus := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef00"
		var res CashConsolidateResult
		// No BearerSecret here: bearer sources are rejected outright (BAD_REQUEST)
		// before the custody lookup this case means to exercise — see S-1 in
		// data/docs/audits/cash-consolidate-2026-08-29/consolidated-findings.md.
		err := callConn.Call(ctxT(t), constants.NIP47MethodCashConsolidate, CashConsolidateParams{
			Sources: []ConsolidateSourceParam{
				consolidateSourceFor(t, wpA, callerPriv, callerPub, newPub, happyPathAmountMloki),
				{WalletPubkey: bogus},
			},
			NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_NOT_FOUND)
	})

	t.Run("UnauthorizedProof_Rejected", func(t *testing.T) {
		wp1, conn1 := mintPubkeySource(t, clientA, callerPub, happyPathAmountMloki)
		wp2, _ := mintPubkeySource(t, clientA, callerPub, happyPathAmountMloki)
		callConn := mustConnect(t, conn1)
		strangerPriv := newTestPrivkey(t) // does NOT own wp1's slice
		var res CashConsolidateResult
		err := callConn.Call(ctxT(t), constants.NIP47MethodCashConsolidate, CashConsolidateParams{
			Sources: []ConsolidateSourceParam{
				consolidateSourceFor(t, wp1, strangerPriv, callerPub, newPub, happyPathAmountMloki),
				consolidateSourceFor(t, wp2, callerPriv, callerPub, newPub, happyPathAmountMloki),
			},
			NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("SourcesDrainedAndNotDoubleSpendable", func(t *testing.T) {
		wp1, conn1 := mintPubkeySource(t, clientA, callerPub, happyPathAmountMloki)
		wp2, conn2 := mintPubkeySource(t, clientA, callerPub, happyPathAmountMloki*2)
		callConn := mustConnect(t, conn1)
		var res CashConsolidateResult
		require.NoError(t, callConn.Call(ctxT(t), constants.NIP47MethodCashConsolidate, CashConsolidateParams{
			Sources: []ConsolidateSourceParam{
				consolidateSourceFor(t, wp1, callerPriv, callerPub, newPub, happyPathAmountMloki),
				consolidateSourceFor(t, wp2, callerPriv, callerPub, newPub, happyPathAmountMloki*2),
			},
			NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		}, &res))
		require.EqualValues(t, happyPathAmountMloki*3, res.AmountMloki)

		// Both source connections drained (their slices were consumed).
		for _, c := range []string{conn1, conn2} {
			src := mustConnect(t, c)
			var bal GetBalanceResult
			require.NoError(t, src.Call(ctxT(t), "get_balance", struct{}{}, &bal))
			assert.EqualValues(t, 0, bal.Balance, "a consolidated source must be drained")
		}

		// Redeeming an already-consolidated source must fail (no double-spend).
		src1 := mustConnect(t, conn1)
		inv := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "consolidate double-spend attempt")
		proof := buildClaimProofEvent(t, callerPriv, wp1, inv.PaymentHash, nil, time.Now())
		var cr ClaimFundsResult
		reErr := src1.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice: inv.Invoice, IdentityType: "pubkey", IdentityValue: callerPub, IdentityEvent: eventJSON(t, proof),
		}, &cr)
		require.Error(t, reErr, "an already-consolidated source must not be redeemable again")

		// The merged wallet redeems for exactly the full sum.
		merged := decryptSplitWallet(t, res.NewWalletPubkey, res.NewWalletToken, newPriv)
		mInv := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki*3, "consolidated redeem")
		mProof := buildClaimProofEvent(t, newPriv, res.NewWalletPubkey, mInv.PaymentHash, nil, time.Now())
		var mcr ClaimFundsResult
		require.NoError(t, merged.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice: mInv.Invoice, IdentityType: "pubkey", IdentityValue: newPub, IdentityEvent: eventJSON(t, mProof),
		}, &mcr))
		require.NotEmpty(t, mcr.Preimage)
	})

	t.Run("ConsolidatedTokenCarriesVerifiableProvenance", func(t *testing.T) {
		wp1, conn1 := mintPubkeySource(t, clientA, callerPub, happyPathAmountMloki)
		wp2, _ := mintPubkeySource(t, clientA, callerPub, happyPathAmountMloki)
		callConn := mustConnect(t, conn1)
		var res CashConsolidateResult
		require.NoError(t, callConn.Call(ctxT(t), constants.NIP47MethodCashConsolidate, CashConsolidateParams{
			Sources: []ConsolidateSourceParam{
				consolidateSourceFor(t, wp1, callerPriv, callerPub, newPub, happyPathAmountMloki),
				consolidateSourceFor(t, wp2, callerPriv, callerPub, newPub, happyPathAmountMloki),
			},
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
			MintSignature: true,
		}, &res))

		merged := decryptSplitWalletToken(t, res.NewWalletPubkey, res.NewWalletToken, newPriv)
		require.NotNil(t, merged.MintSignature, "an opted-in consolidated token must carry provenance")
		require.NotNil(t, merged.AttestedAmount)
		assert.EqualValues(t, happyPathAmountMloki*2, *merged.AttestedAmount, "provenance must attest the merged sum")
		minter, ok := lokicash.VerifyMint(merged)
		require.True(t, ok, "the consolidated token's provenance must verify")
		require.Len(t, minter, 66)
	})
}

// TestProvenance_TamperedTokenFailsVerification takes a REAL node-signed token
// and confirms that altering its attested amount makes VerifyMint recover a
// different key than the untampered token — i.e. an attacker cannot restate a
// signed bill's denomination while keeping valid-looking provenance.
func TestProvenance_TamperedTokenFailsVerification(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "provenance-tamper", nil)
	hubClient := mustConnect(t, hub.Connection)
	recipientPub, err := nostr.GetPublicKey(newTestPrivkey(t))
	require.NoError(t, err)

	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients:    onePubkeyRecipient(recipientPub, happyPathAmountMloki),
		Expiry:        happyPathExpirySecs,
		MintSignature: true,
	}, &created))

	genuine, err := lokicash.Decode(created.CashToken)
	require.NoError(t, err)
	honest, ok := lokicash.VerifyMint(genuine)
	require.True(t, ok)

	// Tamper: inflate the attested amount, keep the real signature.
	inflated := *genuine.AttestedAmount + 1
	tampered := genuine
	tampered.AttestedAmount = &inflated
	recovered, ok := lokicash.VerifyMint(tampered)
	require.True(t, ok, "recovery still runs on a well-formed 65-byte signature")
	assert.NotEqual(t, honest, recovered, "a restated denomination must not recover the true minter")
}
