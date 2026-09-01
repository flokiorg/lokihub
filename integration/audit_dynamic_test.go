//go:build integration

// audit_dynamic_test.go holds NEW black-box scenarios written for the
// 2026-07-28 dynamic-analysis security audit. They drive ONLY the real
// NWC/HTTP surface (never Go internals), as a malicious or compromised holder
// of a shared cash_wallet connection would, probing the cash_transfer / cash_redeem
// slice-accounting guards over real Nostr relay round-trips and real Lightning
// self-payments. Everything here is intentionally adversarial and goes beyond
// the happy-path coverage in cash_transfer_test.go / cash_redeem_test.go.
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

// oneBearerRecipient builds a single-bearer-recipient mint_cash
// request. The Hub mints the bearer secret and returns it once in the create
// response (over the Hub's own single-owner connection).
func oneBearerRecipient(amountMloki uint64) []CashWalletRecipientParam {
	return []CashWalletRecipientParam{{IdentityType: "bearer", AmountMillis: amountMloki}}
}

// createBearerWallet mints a fresh single-recipient bearer Cash wallet and
// returns its shared pairing URI plus the one-time bearer secret.
func createBearerWallet(t *testing.T, hubClient *nwcclient.Client, amountMloki uint64) (pairingURI, walletPubkey, bearerSecret string) {
	t.Helper()
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
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

// TestAudit_CashTransferVsRedeem_NeverBothSucceed races a real cash_redeem
// against a real cash_transfer on the SAME bearer slice, over real relay
// round-trips, and asserts exactly one of them ever wins.
//
// REGRESSION GUARD for a fixed atomicity violation (fixed in e0d4559, see
// data/docs/audits/consolidated-findings-2026-07-28.md "ClaimCashSlice's
// commit didn't re-verify identity"): cash_transfer and cash_redeem used to be
// able to BOTH report success for one slice. Root cause was
// ClaimCashSlice's committing UPDATE being guarded by
// "WHERE id = ? AND claimed_at IS NULL" only — it never re-checked
// identity_type/identity_value. A cash_transfer that committed first
// (reassigning the row's identity without touching claimed_at) let a racing
// cash_redeem, which had read the row a moment earlier, still match on
// (id, claimed_at IS NULL) and pay the ORIGINAL caller — silently voiding the
// transfer the API had just told the new owner had succeeded.
//
// This test previously asserted the OPPOSITE (that the race reproduced) as
// the live characterization of that bug. Re-run against this branch on
// 2026-07-29, it could no longer reproduce a single "both won" outcome across
// 40 iterations — the fix holds under real network round-trips, not just in
// the unit-level race test. It's inverted here into a permanent regression
// guard: hard-fail immediately the moment both sides ever win again.
//
// Attacker model: the current holder A of a bearer slice "sells"/hands it to
// buyer B via cash_transfer while concurrently firing cash_redeem to A's own
// invoice. If both won, B would receive a "transfer succeeded, amount=A"
// response for a slice A simultaneously drained — B's slice unredeemable yet
// the API confirming the handoff.
func TestAudit_CashTransferVsRedeem_NeverBothSucceed(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-redeem-vs-transfer", nil)
	hubClient := mustConnect(t, hub.Connection)

	const iterations = 40

	for i := 0; i < iterations; i++ {
		pairingURI, _, secret1 := createBearerWallet(t, hubClient, happyPathAmountMloki)

		// Two independent clients (each its own relay connection) so the two
		// requests genuinely race on the wire, not through one client's lock.
		redeemClient := mustConnect(t, pairingURI)
		transferClient := mustConnect(t, pairingURI)

		redeemInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "audit redeem-vs-transfer")
		secret2Hex, secret2Hash := bearerSecretAndHash(t)

		var redeemErr, transferErr error
		var redeemRes ClaimFundsResult
		var transferRes CashTransferResult

		fireBarrier(
			func() {
				redeemErr = redeemClient.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
					Invoice:      redeemInvoice.Invoice,
					BearerSecret: secret1,
				}, &redeemRes)
			},
			func() {
				transferErr = transferClient.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
					BearerSecret: secret1,
					NewIdentity:  CashTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: secret2Hash},
				}, &transferRes)
			},
		)

		redeemWon := redeemErr == nil
		transferWon := transferErr == nil
		t.Logf("iter %d: redeemWon=%v (err=%v) transferWon=%v (err=%v)", i, redeemWon, redeemErr, transferWon, transferErr)

		require.True(t, redeemWon || transferWon,
			"neither op succeeded on an unclaimed slice — unexpected (redeemErr=%v transferErr=%v)", redeemErr, transferErr)
		require.False(t, redeemWon && transferWon,
			"REGRESSION: cash_redeem AND cash_transfer both reported success for the same bearer slice "+
				"(redeem preimage=%q, transfer new amount=%d) — the e0d4559 identity re-check fix has regressed",
			redeemRes.Preimage, transferRes.AmountMillis)

		// Whichever side lost must leave no redeemable path of its own:
		if redeemWon {
			// transfer lost — secret2 (the would-be new owner) must never have
			// been registered, since the transfer never took effect.
			probeInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "audit loser-probe-secret2")
			var probeRes ClaimFundsResult
			probeErr := redeemClient.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
				Invoice:      probeInvoice.Invoice,
				BearerSecret: secret2Hex,
			}, &probeRes)
			require.Error(t, probeErr, "CRITICAL DOUBLE-SPEND: the losing transfer's target secret redeemed a slice already paid out")
			requireNWCErrorCode(t, probeErr, constants.ERROR_NOT_FOUND)
		} else {
			// transfer won — the pre-transfer secret1 must never redeem again;
			// only secret2 (the new registered identity) may.
			probeInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "audit loser-probe-secret1")
			var probeRes ClaimFundsResult
			probeErr := redeemClient.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
				Invoice:      probeInvoice.Invoice,
				BearerSecret: secret1,
			}, &probeRes)
			require.Error(t, probeErr, "CRITICAL DOUBLE-SPEND: the superseded pre-transfer secret redeemed a slice already transferred away")
			requireNWCErrorCode(t, probeErr, constants.ERROR_NOT_FOUND)

			winInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "audit winner-probe-secret2")
			var winRes ClaimFundsResult
			require.NoError(t, redeemClient.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
				Invoice:      winInvoice.Invoice,
				BearerSecret: secret2Hex,
			}, &winRes), "the new owner's secret must be able to redeem the slice it was transferred")
			require.NotEmpty(t, winRes.Preimage)
		}
	}
}

// TestAudit_CashConcurrentTransfers_ExactlyOneWinner races two cash_transfer
// calls carrying the SAME current bearer secret but two DIFFERENT new bearer
// commitments, over real relay round-trips. Only one may win; afterward only
// the winning commitment's secret may redeem the slice — never both.
func TestAudit_CashConcurrentTransfers_ExactlyOneWinner(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-concurrent-transfers", nil)
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
					var r CashTransferResult
					aErr = clientA.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
						BearerSecret: secret1,
						NewIdentity:  CashTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: aHash},
					}, &r)
				},
				func() {
					var r CashTransferResult
					bErr = clientB.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
						BearerSecret: secret1,
						NewIdentity:  CashTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: bHash},
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
			err := clientA.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
				Invoice:      loseInvoice.Invoice,
				BearerSecret: loseHex,
			}, &loseRes)
			requireNWCErrorCode(t, err, constants.ERROR_NOT_FOUND)

			// Winner's secret redeems exactly the full amount, once.
			winInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "audit winner-secret")
			var winRes ClaimFundsResult
			require.NoError(t, clientA.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
				Invoice:      winInvoice.Invoice,
				BearerSecret: winHex,
			}, &winRes))
			require.NotEmpty(t, winRes.Preimage)
		})
	}
}

// TestAudit_CashTransferBearerTarget_Boundaries exercises adversarial /
// boundary new_identity payloads against the live cash_transfer handler,
// confirming the shared-connection secret-leak guards (last commit's fix)
// and shape validation actually reject at the wire, not just in unit tests.
func TestAudit_CashTransferBearerTarget_Boundaries(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-transfer-boundaries", nil)
	hubClient := mustConnect(t, hub.Connection)

	// helper: build a fresh pubkey-identity slice we can drive a transfer from
	newPubkeySlice := func(t *testing.T) (shared *nwcclient.Client, curPriv, curPub, walletPubkey string) {
		curPriv = newTestPrivkey(t)
		var err error
		curPub, err = nostr.GetPublicKey(curPriv)
		require.NoError(t, err)
		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(curPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		return mustConnect(t, created.PairingURI), curPriv, curPub, created.WalletPubkey
	}

	t.Run("BearerTarget_MissingIdentityValue_Rejected_NoServerMintedSecret", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newPubkeySlice(t)
		// Proof bound to a bearer target with EMPTY value (what a client asking
		// the server to mint a secret would send).
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "bearer", "", "", happyPathAmountMloki, nil, time.Now())
		var res CashTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: curPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "bearer"}, // no identity_value
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
		require.Empty(t, res.IdentityValue, "server must never mint/return a bearer secret over the shared connection")
	})

	t.Run("BearerTarget_ShortCommitment_Rejected", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newPubkeySlice(t)
		badHash := strings.Repeat("ab", 31) // 62 hex chars = 31 bytes, not 32
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "bearer", badHash, "", happyPathAmountMloki, nil, time.Now())
		var res CashTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: curPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: badHash},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("BearerTarget_NonHexCommitment_Rejected", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newPubkeySlice(t)
		badHash := strings.Repeat("zz", 32) // 64 chars but not hex
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "bearer", badHash, "", happyPathAmountMloki, nil, time.Now())
		var res CashTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: curPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: badHash},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("BearerTarget_WithIAPubkey_Rejected", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newPubkeySlice(t)
		_, secretHash := bearerSecretAndHash(t)
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "bearer", secretHash, "", happyPathAmountMloki, nil, time.Now())
		var res CashTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: curPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: secretHash, IAPubkey: curPub},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("UnknownNewIdentityType_Rejected", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newPubkeySlice(t)
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "totally_bogus", "x", "", happyPathAmountMloki, nil, time.Now())
		var res CashTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: curPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "totally_bogus", IdentityValue: "x"},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("BearerSecret_MixedWithIdentityFields_Rejected", func(t *testing.T) {
		shared, _, curPub, _ := newPubkeySlice(t)
		secretHex, secretHash := bearerSecretAndHash(t)
		var res CashTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			BearerSecret:  secretHex,
			IdentityType:  "pubkey", // illegal alongside bearer_secret
			IdentityValue: curPub,
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: secretHash},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("BearerSecret_NonHex_Rejected", func(t *testing.T) {
		shared, _, _, _ := newPubkeySlice(t)
		_, secretHash := bearerSecretAndHash(t)
		var res CashTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			BearerSecret: "nothexatall!!",
			NewIdentity:  CashTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: secretHash},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("BearerSecret_WrongSecret_NotFound", func(t *testing.T) {
		shared, _, _, _ := newPubkeySlice(t)
		wrongHex, targetHash := bearerSecretAndHash(t) // random, unrelated secret
		var res CashTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			BearerSecret: wrongHex,
			NewIdentity:  CashTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: targetHash},
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_NOT_FOUND)
	})
}

// TestAudit_CashTransferIntoBearer_SpinsOffOnSharedWallet confirms the
// "a bearer slice cannot share a wallet with other recipients" rule now holds
// via spin-off rather than outright rejection at the wire: on a two-recipient
// wallet, transferring one recipient's slice INTO a bearer identity moves that
// slice's value into a brand-new dedicated wallet (NIP-JW "Spinning a slice
// off into a dedicated wallet") instead of converting it in place — a bearer
// slice never ends up sitting anonymously next to a still-identified sibling,
// but the recipient isn't stuck either. Full inner-encryption-exclusivity
// coverage lives in cash_transfer_spinoff_test.go; this only re-confirms the
// wire-level outcome changed from a flat rejection to a real, redeemable new
// connection, since this test used to assert the opposite.
func TestAudit_CashTransferIntoBearer_SpinsOffOnSharedWallet(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-into-bearer-shared", nil)
	hubClient := mustConnect(t, hub.Connection)

	aPriv := newTestPrivkey(t)
	aPub, err := nostr.GetPublicKey(aPriv)
	require.NoError(t, err)
	bPub, err := nostr.GetPublicKey(newTestPrivkey(t))
	require.NoError(t, err)

	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: []CashWalletRecipientParam{
			{IdentityType: "pubkey", IdentityValue: aPub, AmountMillis: happyPathAmountMloki},
			{IdentityType: "pubkey", IdentityValue: bPub, AmountMillis: happyPathAmountMloki},
		},
		Expiry: happyPathExpirySecs,
	}, &created))
	shared := mustConnect(t, created.PairingURI)

	secretHex, secretHash := bearerSecretAndHash(t)
	proof := buildTransferProofEvent(t, aPriv, created.WalletPubkey, "bearer", secretHash, "", happyPathAmountMloki, nil, time.Now())
	var res CashTransferResult
	require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType:  "pubkey",
		IdentityValue: aPub,
		IdentityEvent: eventJSON(t, proof),
		NewIdentity:   CashTransferNewIdentityParam{IdentityType: "bearer", IdentityValue: secretHash},
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
	require.NoError(t, newWalletClient.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:      invoice.Invoice,
		BearerSecret: secretHex,
	}, &claim))
	require.NotEmpty(t, claim.Preimage)
}
