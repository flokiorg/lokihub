//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/lokicash"
)

// TestAuditA_SecB_CarveLocksOutThePreviousHolder is the adversarial counterpart to the
// cash-mode carve fix: it asks whether a previous holder can get at the slice ANYWAY,
// by any route still open to her.
//
// Setup is the real bearer flow. Alice is minted a slice, converts it to cash mode so she
// can gift it as a string, and hands the string to Bob over a channel the Hub never sees.
// Alice therefore keeps a fully working copy of the bill AND its secret — that is what
// bearer means, and it cannot be taken away from her. Bob's only defence is to re-key
// immediately, which is what cashctl's auto-protect does on receive, and what the Hub now
// serves by carving the slice into a wallet whose pairing secret derives from an app ID
// Alice has never seen.
//
// Every assertion below is about what Alice can still do AFTER that carve. The positive
// leg at the end matters just as much: the carve must not have destroyed or stranded the
// value, so Bob redeems for the full amount and real money moves.
func TestAuditA_SecB_CarveLocksOutThePreviousHolder(t *testing.T) {
	cfg := requireConfig(t)
	hub, hubAppID, admin := createEphemeralCashHub(t, cfg, "auditA-secb-bypass", nil)
	hubClient := mustConnect(t, hub.Connection)

	alicePriv := newTestPrivkey(t)
	alicePub := mustPubkey(t, alicePriv)

	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(alicePub, happyPathAmountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))

	// Alice converts to cash mode. In place, correctly: the bill has not changed hands.
	aliceConn := mustConnectBill(t, created.PairingURI, created.CashToken, alicePriv)
	aliceSecret, aliceSecretHash := cashSecretAndHash(t)
	toCashProof := buildTransferProofEvent(t, alicePriv, created.WalletPubkey, "cash", aliceSecretHash, "", happyPathAmountMloki, nil, time.Now())
	var toCash CashTransferResult
	require.NoError(t, aliceConn.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType:  "pubkey",
		IdentityValue: alicePub,
		IdentityEvent: eventJSON(t, toCashProof),
		NewIdentity:   CashTransferNewIdentityParam{IdentityType: "cash", IdentityValue: aliceSecretHash},
	}, &toCash))
	require.Empty(t, toCash.NewWalletToken, "the first conversion must be in place, or this test's premise is gone")

	// Bob receives the string and re-keys at once. The bill has changed hands, so the Hub
	// carves rather than reassigning.
	bobConn := mustConnectBill(t, created.PairingURI, created.CashToken, newTestPrivkey(t))
	bobConn.Bearer()
	bobSecret, bobSecretHash := cashSecretAndHash(t)
	var rekey CashTransferResult
	require.NoError(t, bobConn.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		CashSecret:  aliceSecret,
		NewIdentity: CashTransferNewIdentityParam{IdentityType: "cash", IdentityValue: bobSecretHash},
	}, &rekey))
	require.NotEmpty(t, rekey.NewWalletToken, "a re-key on a bill that has changed hands must carve")
	require.NotEqual(t, created.WalletPubkey, rekey.NewWalletPubkey)

	// The source bill must be genuinely gone, confirmed from the Hub's own records rather
	// than from a client's report — a response cannot prove a deletion happened.
	requireCashWalletDrainedAway(t, admin, hubAppID, created.WalletPubkey, nil)

	// ---- Bypass attempts. Alice still holds the old token AND the old secret. ----

	t.Run("AliceCannotRedeemTheCarvedSlice", func(t *testing.T) {
		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "auditA alice steals")
		// A bearer connection, NOT privateCall: privateCall always attaches a slice proof,
		// and an item carrying both a proof and a cash secret is refused outright by
		// §Bearer Items. That refusal would look like a pass while testing nothing — the
		// request would never reach the question of whether the bill still exists.
		old := mustConnectBill(t, created.PairingURI, created.CashToken, newTestPrivkey(t))
		old.Bearer()
		var claim ClaimFundsResult
		err := old.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice:    invoice.Invoice,
			CashSecret: aliceSecret,
		}, &claim)
		require.Error(t, err, "the previous holder redeemed a slice that had been carved away from her")
		require.Empty(t, claim.Preimage, "no payout may happen on the old bill")
		t.Logf("alice's redeem of the old bill: %v", err)
	})

	t.Run("AliceCannotReKeyItBackToHerself", func(t *testing.T) {
		_, herNewHash := cashSecretAndHash(t)
		old := mustConnectBill(t, created.PairingURI, created.CashToken, newTestPrivkey(t))
		old.Bearer()
		var stolen CashTransferResult
		err := old.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			CashSecret:  aliceSecret,
			NewIdentity: CashTransferNewIdentityParam{IdentityType: "cash", IdentityValue: herNewHash},
		}, &stolen)
		require.Error(t, err, "the previous holder re-keyed a slice that had been carved away from her")
		require.Empty(t, stolen.NewWalletToken)
		t.Logf("alice's re-key of the old bill: %v", err)
	})

	t.Run("TheOldBillNeverNamesTheCarvedWallet", func(t *testing.T) {
		// Whatever the old bill still answers — a tombstone inside its retention window,
		// or nothing at all — it must never point at where the value went. Otherwise the
		// carve would hand its own forwarding address to the party it excluded.
		// privateCall here, not a bearer connection: cash_status takes no cash-secret
		// param, so a bearer item for it cannot be built at all. privateCall hand-builds
		// an item carrying the kind-23193 BILL proof, signed with the connection secret —
		// which is exactly Alice's capability, since she still holds the token. So this
		// asks the right question: what does a token holder learn about a bill that has
		// been carved out from under her?
		var status CashStatusResult
		err := privateCall(t, created.CashToken, constants.NIP47MethodCashStatus,
			CashStatusParams{Scope: "all"}, newTestPrivkey(t), &status)
		if err != nil {
			t.Logf("the old bill does not answer at all: %v", err)
			return
		}
		require.Empty(t, status.Recipients, "a destroyed bill must not still carry a roster")
		for _, r := range status.Recipients {
			require.NotEqual(t, bobSecretHash, r.IdentityValue)
		}
		body := strings.ToLower(status.Error)
		require.NotContains(t, body, strings.ToLower(rekey.NewWalletPubkey),
			"the old bill disclosed the carved wallet's pubkey to a previous holder")
		t.Logf("the old bill answers only: error=%q", status.Error)
	})

	// ---- The positive leg: the value survived, and only Bob can move it. ----

	t.Run("BobRedeemsTheFullAmount", func(t *testing.T) {
		carved, err := lokicash.Decode(rekey.NewWalletToken)
		require.NoError(t, err, "the carved token must be usable by its recipient")
		require.Equal(t, rekey.NewWalletPubkey, carved.WalletPubkey)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "auditA bob redeems carved")
		carvedConn := mustConnectBill(t, nwcURIFromLokicash(carved), rekey.NewWalletToken, newTestPrivkey(t))
		carvedConn.Bearer()
		var claim ClaimFundsResult
		require.NoError(t, carvedConn.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice:    invoice.Invoice,
			CashSecret: bobSecret,
		}, &claim))
		require.NotEmpty(t, claim.Preimage,
			"the carve must preserve the value — a fix that strands the slice is not a fix")
		t.Logf("bob redeemed the carved slice for the full %d mloki", happyPathAmountMloki)
	})
}
