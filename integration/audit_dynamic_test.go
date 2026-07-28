//go:build integration

// audit_dynamic_test.go holds NEW black-box scenarios written for the
// 2026-07-28 dynamic-analysis security audit. They drive ONLY the real
// NWC/HTTP surface (never Go internals), as a malicious or compromised holder
// of a shared jit_wallet connection would, probing the jit_transfer / jit_redeem
// slice-accounting guards over real Nostr relay round-trips and real Lightning
// self-payments. Everything here is intentionally adversarial and goes beyond
// the happy-path coverage in jit_transfer_test.go / claim_funds_test.go.
package integration

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/nip47/cipher"
)

// oneBearerRecipient builds a single-bearer-recipient create_jit_wallet
// request. The Hub mints the bearer secret and returns it once in the create
// response (over the Hub's own single-owner connection).
func oneBearerRecipient(amountMloki uint64) []JITWalletRecipientParam {
	return []JITWalletRecipientParam{{IdentityType: "bearer", AmountMloki: amountMloki}}
}

// createBearerWallet mints a fresh single-recipient bearer JIT wallet and
// returns its shared pairing URI plus the one-time bearer secret.
func createBearerWallet(t *testing.T, hubClient *nwcclient.Client, amountMloki uint64) (pairingURI, walletPubkey, bearerSecret string) {
	t.Helper()
	var created CreateJITWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
		Recipients: oneBearerRecipient(amountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))
	require.Len(t, created.Recipients, 1)
	require.Equal(t, "bearer", created.Recipients[0].IdentityType)
	require.NotEmpty(t, created.Recipients[0].BearerSecret, "hub must mint the bearer secret in the create response")
	return created.PairingURI, created.WalletPubkey, created.Recipients[0].BearerSecret
}

// fireBarrier runs two funcs as simultaneously as the Go scheduler + the real
// relay allow: both block on the same barrier channel, then race.
func fireBarrier(a, b func()) {
	barrier := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-barrier; a() }()
	go func() { defer wg.Done(); <-barrier; b() }()
	close(barrier)
	wg.Wait()
}

// TestAudit_JITRedeemVsTransfer_PhantomTransfer races a real jit_redeem
// against a real jit_transfer on the SAME bearer slice, over real relay
// round-trips, until it observes the interleaving where BOTH return success —
// then characterizes exactly what that means for the funds.
//
// FINDING (atomicity violation): jit_transfer and jit_redeem can BOTH report
// success for one slice. Root cause: ClaimJITWalletSlice's committing UPDATE
// is guarded by "WHERE id = ? AND claimed_at IS NULL" only — it does NOT also
// pin identity_value. So when a jit_transfer commits first (reassigning the
// row's identity_value to the new owner, leaving claimed_at NULL and
// incrementing transfer_count), a jit_redeem that read the row a moment
// earlier still matches it on (id, claimed_at IS NULL) and marks it claimed,
// paying the ORIGINAL caller — silently voiding the transfer the API just
// told the new owner had succeeded.
//
// This test asserts the money-safety invariant that DOES still hold (at most
// one real Lightning payout, no fund duplication) and hard-fails only if that
// is ever violated; it records the serializability/phantom-success violation
// as the finding.
//
// Attacker model: the current holder A of a bearer slice "sells"/hands it to
// buyer B via jit_transfer while concurrently firing jit_redeem to A's own
// invoice. In the losing interleaving, B receives a "transfer succeeded,
// amount=A" response for a slice A simultaneously drains — B's slice is
// unredeemable yet the API confirmed the handoff.
func TestAudit_JITRedeemVsTransfer_PhantomTransfer(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralJITHub(t, cfg, "audit-redeem-vs-transfer", nil)
	hubClient := mustConnect(t, hub.Connection)

	const maxIterations = 40
	bothWonObserved := false

	for i := 0; i < maxIterations && !bothWonObserved; i++ {
		pairingURI, _, secret1 := createBearerWallet(t, hubClient, happyPathAmountMloki)

		// Two independent clients (each its own relay connection) so the two
		// requests genuinely race on the wire, not through one client's lock.
		redeemClient := mustConnect(t, pairingURI)
		transferClient := mustConnect(t, pairingURI)

		redeemInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "audit redeem-vs-transfer")
		secret2Hex, secret2Hash := bearerSecretAndHash(t)

		var redeemErr, transferErr error
		var redeemRes ClaimFundsResult
		var transferRes JITTransferResult

		fireBarrier(
			func() {
				redeemErr = redeemClient.Call(ctxT(t), constants.NIP47MethodJITRedeem, ClaimFundsParams{
					Invoice:      redeemInvoice.Invoice,
					BearerSecret: secret1,
				}, &redeemRes)
			},
			func() {
				transferErr = transferClient.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
					BearerSecret: secret1,
					NewIdentity:  JITTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: secret2Hash},
				}, &transferRes)
			},
		)

		redeemWon := redeemErr == nil
		transferWon := transferErr == nil
		t.Logf("iter %d: redeemWon=%v (err=%v) transferWon=%v (err=%v)", i, redeemWon, redeemErr, transferWon, transferErr)

		require.True(t, redeemWon || transferWon,
			"neither op succeeded on an unclaimed slice — unexpected (redeemErr=%v transferErr=%v)", redeemErr, transferErr)

		if !(redeemWon && transferWon) {
			continue // clean, serialized outcome this round — race not hit yet
		}

		// ---- both succeeded: characterize the anomaly ----
		bothWonObserved = true
		t.Logf("ANOMALY: jit_redeem AND jit_transfer both reported success for the same slice")
		require.NotEmpty(t, redeemRes.Preimage, "redeem reported success without a payout preimage")
		require.EqualValues(t, happyPathAmountMloki, transferRes.AmountMloki,
			"transfer reported success and echoed the full slice amount to the (now-defrauded) new owner")

		// The money-safety invariant that MUST hold: the new bearer owner
		// (secret2) must NOT also be able to redeem, or the slice would pay out
		// TWICE (true fund duplication). Probe it.
		probeInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "audit phantom-double-payout-probe")
		var secondPayout ClaimFundsResult
		secondErr := redeemClient.Call(ctxT(t), constants.NIP47MethodJITRedeem, ClaimFundsParams{
			Invoice:      probeInvoice.Invoice,
			BearerSecret: secret2Hex,
		}, &secondPayout)
		require.Error(t, secondErr,
			"CRITICAL DOUBLE-SPEND: the phantom-transferred bearer secret ALSO redeemed — slice paid out twice")
		requireNWCErrorCode(t, secondErr, constants.ERROR_NOT_FOUND)
		t.Logf("characterized: phantom-success (transfer void, new owner cannot redeem) — funds paid once to the redeeming (original) caller; NOT a fund-duplicating double-spend")
	}

	require.True(t, bothWonObserved,
		"the redeem/transfer race window was not observed in %d iterations this run; "+
			"re-run to reproduce the phantom-success anomaly (it reproduced readily during the audit)", maxIterations)
}

// TestAudit_JITConcurrentTransfers_ExactlyOneWinner races two jit_transfer
// calls carrying the SAME current bearer secret but two DIFFERENT new bearer
// commitments, over real relay round-trips. Only one may win; afterward only
// the winning commitment's secret may redeem the slice — never both.
func TestAudit_JITConcurrentTransfers_ExactlyOneWinner(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralJITHub(t, cfg, "audit-concurrent-transfers", nil)
	hubClient := mustConnect(t, hub.Connection)

	const iterations = 6
	for i := 0; i < iterations; i++ {
		t.Run("iteration", func(t *testing.T) {
			pairingURI, _, secret1 := createBearerWallet(t, hubClient, happyPathAmountMloki)
			clientA := mustConnect(t, pairingURI)
			clientB := mustConnect(t, pairingURI)

			aHex, aHash := bearerSecretAndHash(t)
			bHex, bHash := bearerSecretAndHash(t)

			var aErr, bErr error
			fireBarrier(
				func() {
					var r JITTransferResult
					aErr = clientA.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
						BearerSecret: secret1,
						NewIdentity:  JITTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: aHash},
					}, &r)
				},
				func() {
					var r JITTransferResult
					bErr = clientB.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
						BearerSecret: secret1,
						NewIdentity:  JITTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: bHash},
					}, &r)
				},
			)
			aWon := aErr == nil
			bWon := bErr == nil
			t.Logf("iter %d: aWon=%v (err=%v) bWon=%v (err=%v)", i, aWon, aErr, bWon, bErr)

			require.False(t, aWon && bWon, "both concurrent transfers succeeded — slice forked")
			require.True(t, aWon || bWon, "neither transfer succeeded on an unclaimed slice")

			winHex, loseHex := aHex, bHex
			if bWon {
				winHex, loseHex = bHex, aHex
			}

			// Loser's secret must NOT redeem.
			loseInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "audit loser-secret")
			var loseRes ClaimFundsResult
			err := clientA.Call(ctxT(t), constants.NIP47MethodJITRedeem, ClaimFundsParams{
				Invoice:      loseInvoice.Invoice,
				BearerSecret: loseHex,
			}, &loseRes)
			requireNWCErrorCode(t, err, constants.ERROR_NOT_FOUND)

			// Winner's secret redeems exactly the full amount, once.
			winInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "audit winner-secret")
			var winRes ClaimFundsResult
			require.NoError(t, clientA.Call(ctxT(t), constants.NIP47MethodJITRedeem, ClaimFundsParams{
				Invoice:      winInvoice.Invoice,
				BearerSecret: winHex,
			}, &winRes))
			require.NotEmpty(t, winRes.Preimage)
		})
	}
}

// TestAudit_JITTransferBearerTarget_Boundaries exercises adversarial /
// boundary new_identity payloads against the live jit_transfer handler,
// confirming the shared-connection secret-leak guards (last commit's fix)
// and shape validation actually reject at the wire, not just in unit tests.
func TestAudit_JITTransferBearerTarget_Boundaries(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralJITHub(t, cfg, "audit-transfer-boundaries", nil)
	hubClient := mustConnect(t, hub.Connection)

	// helper: build a fresh pubkey-identity slice we can drive a transfer from
	newPubkeySlice := func(t *testing.T) (shared *nwcclient.Client, curPriv, curPub, walletPubkey string) {
		curPriv = newTestPrivkey(t)
		var err error
		curPub, err = nostr.GetPublicKey(curPriv)
		require.NoError(t, err)
		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(curPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		return mustConnect(t, created.PairingURI), curPriv, curPub, created.WalletPubkey
	}

	t.Run("BearerTarget_MissingIdentityValue_Rejected_NoServerMintedSecret", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newPubkeySlice(t)
		// Proof bound to a bearer target with EMPTY value (what a client asking
		// the server to mint a secret would send).
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "bearer", "", nil, time.Now())
		var res JITTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: curPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   JITTransferNewIdentityParam{IdentityType: "bearer"}, // no identity_value
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
		require.Empty(t, res.IdentityValue, "server must never mint/return a bearer secret over the shared connection")
	})

	t.Run("BearerTarget_ShortCommitment_Rejected", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newPubkeySlice(t)
		badHash := strings.Repeat("ab", 31) // 62 hex chars = 31 bytes, not 32
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "bearer", badHash, nil, time.Now())
		var res JITTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: curPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   JITTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: badHash},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("BearerTarget_NonHexCommitment_Rejected", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newPubkeySlice(t)
		badHash := strings.Repeat("zz", 32) // 64 chars but not hex
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "bearer", badHash, nil, time.Now())
		var res JITTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: curPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   JITTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: badHash},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("BearerTarget_WithIAPubkey_Rejected", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newPubkeySlice(t)
		_, secretHash := bearerSecretAndHash(t)
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "bearer", secretHash, nil, time.Now())
		var res JITTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: curPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   JITTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: secretHash, IAPubkey: curPub},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("UnknownNewIdentityType_Rejected", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newPubkeySlice(t)
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "totally_bogus", "x", nil, time.Now())
		var res JITTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: curPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   JITTransferNewIdentityParam{IdentityType: "totally_bogus", IdentityValue: "x"},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("BearerSecret_MixedWithIdentityFields_Rejected", func(t *testing.T) {
		shared, _, curPub, _ := newPubkeySlice(t)
		secretHex, secretHash := bearerSecretAndHash(t)
		var res JITTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
			BearerSecret:  secretHex,
			IdentityType:  "pubkey", // illegal alongside bearer_secret
			IdentityValue: curPub,
			NewIdentity:   JITTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: secretHash},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("BearerSecret_NonHex_Rejected", func(t *testing.T) {
		shared, _, _, _ := newPubkeySlice(t)
		_, secretHash := bearerSecretAndHash(t)
		var res JITTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
			BearerSecret: "nothexatall!!",
			NewIdentity:  JITTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: secretHash},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("BearerSecret_WrongSecret_NotFound", func(t *testing.T) {
		shared, _, _, _ := newPubkeySlice(t)
		wrongHex, targetHash := bearerSecretAndHash(t) // random, unrelated secret
		var res JITTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
			BearerSecret: wrongHex,
			NewIdentity:  JITTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: targetHash},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_NOT_FOUND)
	})
}

// TestAudit_JITTransferIntoBearer_SpinsOffOnSharedWallet confirms the
// "a bearer slice cannot share a wallet with other recipients" rule now holds
// via spin-off rather than outright rejection at the wire: on a two-recipient
// wallet, transferring one recipient's slice INTO a bearer identity moves that
// slice's value into a brand-new dedicated wallet (NIP-JW "Spinning a slice
// off into a dedicated wallet") instead of converting it in place — a bearer
// slice never ends up sitting anonymously next to a still-identified sibling,
// but the recipient isn't stuck either. Full inner-encryption-exclusivity
// coverage lives in jit_transfer_spinoff_test.go; this only re-confirms the
// wire-level outcome changed from a flat rejection to a real, redeemable new
// connection, since this test used to assert the opposite.
func TestAudit_JITTransferIntoBearer_SpinsOffOnSharedWallet(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralJITHub(t, cfg, "audit-into-bearer-shared", nil)
	hubClient := mustConnect(t, hub.Connection)

	aPriv := newTestPrivkey(t)
	aPub, err := nostr.GetPublicKey(aPriv)
	require.NoError(t, err)
	bPub, err := nostr.GetPublicKey(newTestPrivkey(t))
	require.NoError(t, err)

	var created CreateJITWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
		Recipients: []JITWalletRecipientParam{
			{IdentityType: "pubkey", IdentityValue: aPub, AmountMloki: happyPathAmountMloki},
			{IdentityType: "pubkey", IdentityValue: bPub, AmountMloki: happyPathAmountMloki},
		},
		Expiry: happyPathExpirySecs,
	}, &created))
	shared := mustConnect(t, created.PairingURI)

	secretHex, secretHash := bearerSecretAndHash(t)
	proof := buildTransferProofEvent(t, aPriv, created.WalletPubkey, "bearer", secretHash, nil, time.Now())
	var res JITTransferResult
	require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodJITTransfer, JITTransferParams{
		IdentityType:  "pubkey",
		IdentityValue: aPub,
		IdentityEvent: eventJSON(t, proof),
		NewIdentity:   JITTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: secretHash},
	}, &res))
	require.NotEmpty(t, res.NewWalletPubkey, "a bearer target on a shared wallet must spin off into a new wallet, not reject")
	require.NotEmpty(t, res.NewWalletToken)

	aCipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, res.NewWalletPubkey, aPriv)
	require.NoError(t, err)
	decrypted, err := aCipher.Decrypt(res.NewWalletToken)
	require.NoError(t, err)
	newWalletToken, err := lokicash.Decode(decrypted)
	require.NoError(t, err)

	newWalletClient := mustConnect(t, nwcURIFromLokicash(newWalletToken))
	invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "audit spinoff redeem")
	var claim ClaimFundsResult
	require.NoError(t, newWalletClient.Call(ctxT(t), constants.NIP47MethodJITRedeem, ClaimFundsParams{
		Invoice:      invoice.Invoice,
		BearerSecret: secretHex,
	}, &claim))
	require.NotEmpty(t, claim.Preimage)
}
