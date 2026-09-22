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

// consolidateSourceForCash is consolidateSourceFor's counterpart for a
// cash-mode new_identity target: the proof must bind to identity type "cash"
// + the commitment hash (not "pubkey"), matching what the server actually
// hashes to verify it (nip47/controllers/cash_transfer_controller.go's
// newIdentityHash) — reusing consolidateSourceFor's hardcoded "pubkey"
// binding here would sign a proof for the wrong new_identity and always be
// rejected.
func consolidateSourceForCash(t *testing.T, walletPubkey, proofSignerPriv, callerPub, cashHash string, amount uint64) ConsolidateSourceParam {
	t.Helper()
	proof := buildTransferProofEvent(t, proofSignerPriv, walletPubkey, "cash", cashHash, "", amount, nil, time.Now())
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
		// No CashSecret here: cash-mode sources are rejected outright (BAD_REQUEST)
		// before the custody lookup this case means to exercise.
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
		require.EqualValues(t, happyPathAmountMloki*3, res.AmountMillis)

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

		// The merged wallet redeems for exactly the full sum. Delivery is
		// nested-encrypted to the CALLER (same as cash_transfer's own
		// spin-off delivery), not to new_identity — callerPriv decrypts it.
		merged := decryptSplitWallet(t, res.NewWalletPubkey, res.NewWalletToken, callerPriv)
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

		merged := decryptSplitWalletToken(t, res.NewWalletPubkey, res.NewWalletToken, callerPriv)
		require.NotNil(t, merged.MintSignature, "an opted-in consolidated token must carry provenance")
		require.NotNil(t, merged.AttestedAmount)
		assert.EqualValues(t, happyPathAmountMloki*2, *merged.AttestedAmount, "provenance must attest the merged sum")
		minter, ok := lokicash.VerifyMint(merged)
		require.True(t, ok, "the consolidated token's provenance must verify")
		require.Len(t, minter, 66)
	})

	// CashTargetSecretNotInResponse is the stronger assertion the doc
	// flagged as missing for cash_transfer's own cash-mode target: not just that
	// consolidating to a cash-mode target succeeds, but that the raw secret the
	// caller generated locally never appears anywhere in the wire response —
	// only its commitment hash was ever sent, and the node has no way to
	// mint/return the secret itself.
	t.Run("CashTargetSecretNotInResponse", func(t *testing.T) {
		wp1, conn1 := mintPubkeySource(t, clientA, callerPub, happyPathAmountMloki)
		wp2, _ := mintPubkeySource(t, clientA, callerPub, happyPathAmountMloki)
		callConn := mustConnect(t, conn1)

		cashSecret, cashHash := cashSecretAndHash(t)
		var res CashConsolidateResult
		require.NoError(t, callConn.Call(ctxT(t), constants.NIP47MethodCashConsolidate, CashConsolidateParams{
			Sources: []ConsolidateSourceParam{
				consolidateSourceForCash(t, wp1, callerPriv, callerPub, cashHash, happyPathAmountMloki),
				consolidateSourceForCash(t, wp2, callerPriv, callerPub, cashHash, happyPathAmountMloki),
			},
			NewIdentity: CashTransferNewIdentityParam{IdentityType: "cash", IdentityValue: cashHash},
		}, &res))

		require.NotEmpty(t, res.NewWalletToken)
		assert.NotContains(t, res.NewWalletToken, cashSecret,
			"the wire response must never contain the raw secret the caller generated locally")

		// The delivered token is a plain lokicash1... string (no ECDH target
		// exists for a cash-mode commitment), so it must decode directly — no
		// nested-encryption layer to strip first, unlike a pubkey target.
		decoded, err := lokicash.Decode(res.NewWalletToken)
		require.NoError(t, err, "cash-mode target delivery must be a plain, directly-decodable token")
		assert.Equal(t, res.NewWalletPubkey, decoded.WalletPubkey)

		// The secret actually redeems the merged total, over the cash-mode
		// wallet's own connection (built from the decoded token, same as any
		// other spun-off wallet — nwcclient.Connect needs a
		// nostr+walletconnect:// URI, not the raw lokicash1... token itself;
		// the secret travels as its own request field, never embedded in the
		// connection string — same pattern createCashModeWallet's callers use).
		cashClient := mustConnect(t, nwcURIFromLokicash(decoded))
		mInv := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki*2, "cash-mode target redeem")
		var rr ClaimFundsResult
		require.NoError(t, cashClient.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice: mInv.Invoice, CashSecret: cashSecret,
		}, &rr))
		require.NotEmpty(t, rr.Preimage)
	})

	// CashTargetCommitmentReuse_BothSucceed documents the accepted property
	// flagged in the doc: nothing stops the same commitment hash being
	// submitted as a cash-mode new_identity in two unrelated consolidate calls —
	// harmless, since only the secret's generator ever knows it, and each
	// resulting wallet is independently funded and independently redeemable.
	t.Run("CashTargetCommitmentReuse_BothSucceed", func(t *testing.T) {
		_, cashHash := cashSecretAndHash(t)

		consolidateOnce := func(t *testing.T) CashConsolidateResult {
			t.Helper()
			wp1, conn1 := mintPubkeySource(t, clientA, callerPub, happyPathAmountMloki)
			wp2, _ := mintPubkeySource(t, clientA, callerPub, happyPathAmountMloki)
			callConn := mustConnect(t, conn1)
			var res CashConsolidateResult
			require.NoError(t, callConn.Call(ctxT(t), constants.NIP47MethodCashConsolidate, CashConsolidateParams{
				Sources: []ConsolidateSourceParam{
					consolidateSourceForCash(t, wp1, callerPriv, callerPub, cashHash, happyPathAmountMloki),
					consolidateSourceForCash(t, wp2, callerPriv, callerPub, cashHash, happyPathAmountMloki),
				},
				NewIdentity: CashTransferNewIdentityParam{IdentityType: "cash", IdentityValue: cashHash},
			}, &res))
			return res
		}

		first := consolidateOnce(t)
		second := consolidateOnce(t)
		assert.NotEqual(t, first.NewWalletPubkey, second.NewWalletPubkey,
			"reusing a commitment must produce two independent wallets, not collide")
	})
}

// TestAudit_CashConsolidateConnectionKey_RevokedIA_Rejected: trust the IA,
// consolidate to that connection_key target successfully, revoke the IA,
// attempt redemption, assert it now fails clean — directly exercising the
// "checked live again at redemption" claim in the doc, not just asserting it
// in prose. Mirrors TestAudit_CashTransferConnectionKey_RevokedIA_Rejected's
// admin-client setup in cash_transfer_audit_connection_key_test.go.
func TestAudit_CashConsolidateConnectionKey_RevokedIA_Rejected(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured")
	}
	iaPriv := createEphemeralTrustedIA(t, cfg)
	iaPub := mustPubkey(t, iaPriv)
	hub, _, _ := createEphemeralCashHub(t, cfg, "consolidate-connkey-revoked-ia", nil)
	hubClient := mustConnect(t, hub.Connection)

	callerPriv := newTestPrivkey(t)
	callerPub := mustPubkey(t, callerPriv)
	connectionKey := newTestConnectionKey(t)
	claimantPriv := newTestPrivkey(t)
	claimantPub := mustPubkey(t, claimantPriv)

	wp1, conn1 := mintPubkeySource(t, hubClient, callerPub, happyPathAmountMloki)
	wp2, _ := mintPubkeySource(t, hubClient, callerPub, happyPathAmountMloki)
	callConn := mustConnect(t, conn1)

	proof1 := buildTransferProofEvent(t, callerPriv, wp1, "connection_key", connectionKey, iaPub, happyPathAmountMloki, nil, time.Now())
	proof2 := buildTransferProofEvent(t, callerPriv, wp2, "connection_key", connectionKey, iaPub, happyPathAmountMloki, nil, time.Now())

	var res CashConsolidateResult
	require.NoError(t, callConn.Call(ctxT(t), constants.NIP47MethodCashConsolidate, CashConsolidateParams{
		Sources: []ConsolidateSourceParam{
			{WalletPubkey: wp1, IdentityType: "pubkey", IdentityValue: callerPub, IdentityEvent: eventJSON(t, proof1)},
			{WalletPubkey: wp2, IdentityType: "pubkey", IdentityValue: callerPub, IdentityEvent: eventJSON(t, proof2)},
		},
		NewIdentity: CashTransferNewIdentityParam{IdentityType: "connection_key", IdentityValue: connectionKey, IAPubkey: iaPub},
	}, &res))
	require.NotEmpty(t, res.NewWalletToken)

	decoded, err := lokicash.Decode(res.NewWalletToken)
	require.NoError(t, err, "connection_key target delivery must be a plain, directly-decodable token")
	assert.Equal(t, res.NewWalletPubkey, decoded.WalletPubkey)

	// Revoke the IA now that the merge itself has succeeded.
	require.NoError(t, admin.deleteIdentityAuthority(iaPub))
	t.Cleanup(func() {
		// Re-register so any admin-side ephemeral cleanup can still reclaim
		// funds on the ordinary path (mirrors the sibling transfer test).
		_ = admin.registerIdentityAuthority(iaPub, ephemeralFixtureNamePrefix+" re-trusted for cleanup")
	})

	attestation := buildIAAttestationEvent(t, iaPriv, connectionKey, claimantPub, time.Hour)
	mInv := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki*2, "revoked ia redeem attempt")
	redeemProof := buildClaimProofEvent(t, claimantPriv, res.NewWalletPubkey, mInv.PaymentHash,
		connKeyTransferProofTags(connectionKey, attestation.ID), time.Now())

	mergedClient := mustConnect(t, nwcURIFromLokicash(decoded))
	var rr ClaimFundsResult
	err = mergedClient.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice: mInv.Invoice, IdentityType: "connection_key", IdentityValue: connectionKey,
		IdentityEvent: eventJSON(t, redeemProof), AttestationEvent: eventJSON(t, attestation),
	}, &rr)
	requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
	require.ErrorContains(t, err, "revoked")
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

// TestAudit_CashConsolidateConnectionKeySource_HappyPath: a mixed batch (one
// pubkey source, one connection_key source, both proven live over real relay
// round-trips) merges into one pubkey-owned wallet, over the real running
// backend — the black-box counterpart to
// TestHandleCashConsolidateEvent_ConnectionKeySource_HappyPath's in-process
// unit coverage.
func TestAudit_CashConsolidateConnectionKeySource_HappyPath(t *testing.T) {
	cfg := requireConfig(t)
	iaPriv := createEphemeralTrustedIA(t, cfg)
	iaPub := mustPubkey(t, iaPriv)
	hub, _, _ := createEphemeralCashHub(t, cfg, "consolidate-connkey-source", nil)
	hubClient := mustConnect(t, hub.Connection)

	callerPriv := newTestPrivkey(t)
	callerPub := mustPubkey(t, callerPriv)
	newPub := mustPubkey(t, newTestPrivkey(t))

	wp1, conn1 := mintPubkeySource(t, hubClient, callerPub, happyPathAmountMloki)
	callConn := mustConnect(t, conn1)

	connectionKey := newTestConnectionKey(t)
	_, wp2, claimantPriv, claimantPub := createConnKeyCashWallet(t, hubClient, iaPub, connectionKey, happyPathAmountMloki)

	proof1 := buildTransferProofEvent(t, callerPriv, wp1, "pubkey", newPub, "", happyPathAmountMloki, nil, time.Now())
	attestation := buildIAAttestationEvent(t, iaPriv, connectionKey, claimantPub, time.Hour)
	proof2 := buildTransferProofEvent(t, claimantPriv, wp2, "pubkey", newPub, "", happyPathAmountMloki,
		connKeyTransferProofTags(connectionKey, attestation.ID), time.Now())

	var res CashConsolidateResult
	require.NoError(t, callConn.Call(ctxT(t), constants.NIP47MethodCashConsolidate, CashConsolidateParams{
		Sources: []ConsolidateSourceParam{
			{WalletPubkey: wp1, IdentityType: "pubkey", IdentityValue: callerPub, IdentityEvent: eventJSON(t, proof1)},
			{
				WalletPubkey: wp2, IdentityType: "connection_key", IdentityValue: connectionKey,
				IdentityEvent: eventJSON(t, proof2), AttestationEvent: eventJSON(t, attestation),
			},
		},
		NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
	}, &res))
	assert.EqualValues(t, happyPathAmountMloki*2, res.AmountMillis)
}

// TestAudit_CashConsolidateConnectionKey_AttestationReplayAcrossBatches_Allowed
// confirms presenting the same long-lived attestation in two unrelated
// consolidate calls is accepted both times — documenting the intentional
// non-single-use property (an attestation proves "this pubkey owns this
// connection_key", not a one-shot spend authorization) so a future change
// doesn't accidentally break it thinking it's a bug.
func TestAudit_CashConsolidateConnectionKey_AttestationReplayAcrossBatches_Allowed(t *testing.T) {
	cfg := requireConfig(t)
	iaPriv := createEphemeralTrustedIA(t, cfg)
	iaPub := mustPubkey(t, iaPriv)
	hub, _, _ := createEphemeralCashHub(t, cfg, "consolidate-connkey-attestation-reuse", nil)
	hubClient := mustConnect(t, hub.Connection)

	callerPriv := newTestPrivkey(t)
	callerPub := mustPubkey(t, callerPriv)
	connectionKey := newTestConnectionKey(t)
	_, walletPubkey1, claimantPriv, claimantPub := createConnKeyCashWallet(t, hubClient, iaPub, connectionKey, happyPathAmountMloki)
	attestation := buildIAAttestationEvent(t, iaPriv, connectionKey, claimantPub, time.Hour)

	consolidateOnce := func(t *testing.T, connWalletPubkey string) CashConsolidateResult {
		t.Helper()
		wpPubkey, connPubkey := mintPubkeySource(t, hubClient, callerPub, happyPathAmountMloki)
		callConn := mustConnect(t, connPubkey)
		newPub := mustPubkey(t, newTestPrivkey(t))
		proof1 := buildTransferProofEvent(t, callerPriv, wpPubkey, "pubkey", newPub, "", happyPathAmountMloki, nil, time.Now())
		proof2 := buildTransferProofEvent(t, claimantPriv, connWalletPubkey, "pubkey", newPub, "", happyPathAmountMloki,
			connKeyTransferProofTags(connectionKey, attestation.ID), time.Now())
		var res CashConsolidateResult
		require.NoError(t, callConn.Call(ctxT(t), constants.NIP47MethodCashConsolidate, CashConsolidateParams{
			Sources: []ConsolidateSourceParam{
				{WalletPubkey: wpPubkey, IdentityType: "pubkey", IdentityValue: callerPub, IdentityEvent: eventJSON(t, proof1)},
				{
					WalletPubkey: connWalletPubkey, IdentityType: "connection_key", IdentityValue: connectionKey,
					IdentityEvent: eventJSON(t, proof2), AttestationEvent: eventJSON(t, attestation),
				},
			},
			NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		}, &res))
		return res
	}

	res1 := consolidateOnce(t, walletPubkey1)
	assert.EqualValues(t, happyPathAmountMloki*2, res1.AmountMillis)

	// A second, unrelated wallet under the SAME connection_key, claimed by the
	// SAME claimant keypair the attestation vouches for — minted directly
	// (not via createConnKeyCashWallet, which would generate an unrelated
	// fresh claimant the existing attestation doesn't cover).
	var created2 MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: []CashWalletRecipientParam{
			{IdentityType: "connection_key", IdentityValue: connectionKey, IAPubkey: iaPub, AmountMillis: happyPathAmountMloki},
		},
		Expiry: happyPathExpirySecs,
	}, &created2))

	res2 := consolidateOnce(t, created2.WalletPubkey)
	assert.EqualValues(t, happyPathAmountMloki*2, res2.AmountMillis)
}
