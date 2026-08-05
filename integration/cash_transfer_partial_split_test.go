//go:build integration

// cash_transfer_partial_split_test.go covers the NEW cash_transfer capability
// added alongside the JIT-Wallet-to-Cash-Hub rename: carving a PARTIAL amount
// off a slice into a brand-new dedicated cash_wallet, leaving the remainder
// behind under the same identity — NIP-CASH "Splitting a Slice." End to end
// over a real Nostr relay against a real running instance, the black-box
// counterpart to nip47/controllers/cash_transfer_controller_test.go's
// TestHandleCashTransferEvent_PartialSplit_* unit coverage.
package integration

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/nip47/cipher"
)

func TestCashTransferPartialSplit(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "cash-transfer-partial-split-hub", nil)
	hubClient := mustConnect(t, hub.Connection)

	t.Run("HappyPath_RemainderStaysRedeemable_NewWalletRedeemable", func(t *testing.T) {
		currentPriv := newTestPrivkey(t)
		currentPub, err := nostr.GetPublicKey(currentPriv)
		require.NoError(t, err)

		const fullAmount = happyPathAmountMloki * 5
		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(currentPub, fullAmount),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		newPriv := newTestPrivkey(t)
		newPub, err := nostr.GetPublicKey(newPriv)
		require.NoError(t, err)

		splitAmount := uint64(happyPathAmountMloki * 2)
		proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "pubkey", newPub, "", splitAmount, nil, time.Now())
		var transferResult CashTransferResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: currentPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
			AmountMloki:   &splitAmount,
		}, &transferResult))
		require.Equal(t, splitAmount, transferResult.AmountMloki)
		require.NotNil(t, transferResult.RemainingAmountMloki)
		require.EqualValues(t, fullAmount-int64(splitAmount), *transferResult.RemainingAmountMloki) //nolint:gosec
		require.NotEmpty(t, transferResult.NewWalletPubkey)
		require.NotEmpty(t, transferResult.NewWalletToken, "a partial split must always mint a new dedicated wallet")

		// The ORIGINAL identity must still be able to redeem the REMAINDER on
		// the SAME connection — a partial split never reassigns the source.
		remainderInvoice := mintInvoiceFromSimpleWallet(t, cfg, uint64(fullAmount)-splitAmount, "partial split remainder redeem")
		remainderProof := buildClaimProofEvent(t, currentPriv, created.WalletPubkey, remainderInvoice.PaymentHash, nil, time.Now())
		var remainderClaim ClaimFundsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice:       remainderInvoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: currentPub,
			IdentityEvent: eventJSON(t, remainderProof),
		}, &remainderClaim))
		require.NotEmpty(t, remainderClaim.Preimage)

		// The inner layer is keyed to the CALLER's own identity (currentPriv,
		// the one who just proved ownership via identity_event) — not the
		// new recipient's — since the caller is who actually receives this
		// API response and is responsible for relaying the decoded token to
		// newPub out of band (NIP-CASH "Spinning a Slice Off Into a
		// Dedicated Wallet"). Decrypted here the same way the real caller
		// would: their own privkey + the plaintext NewWalletPubkey, never a
		// DB lookup.
		newWalletCipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, transferResult.NewWalletPubkey, currentPriv)
		require.NoError(t, err)
		decrypted, err := newWalletCipher.Decrypt(transferResult.NewWalletToken)
		require.NoError(t, err)
		newWalletToken, err := lokicash.Decode(decrypted)
		require.NoError(t, err)

		newWalletClient := mustConnect(t, nwcURIFromLokicash(newWalletToken))
		newWalletInvoice := mintInvoiceFromSimpleWallet(t, cfg, splitAmount, "partial split new wallet redeem")
		newWalletProof := buildClaimProofEvent(t, newPriv, transferResult.NewWalletPubkey, newWalletInvoice.PaymentHash, nil, time.Now())
		var newWalletClaim ClaimFundsResult
		require.NoError(t, newWalletClient.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice:       newWalletInvoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: newPub,
			IdentityEvent: eventJSON(t, newWalletProof),
		}, &newWalletClaim))
		require.NotEmpty(t, newWalletClaim.Preimage)
	})

	t.Run("ExceedsSliceBalance_Rejected", func(t *testing.T) {
		currentPriv := newTestPrivkey(t)
		currentPub, err := nostr.GetPublicKey(currentPriv)
		require.NoError(t, err)

		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(currentPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		newPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		tooMuch := uint64(happyPathAmountMloki + 1)
		proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "pubkey", newPub, "", tooMuch, nil, time.Now())
		var transferResult CashTransferResult
		err = shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: currentPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
			AmountMloki:   &tooMuch,
		}, &transferResult)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})
}

// TestCashTransferMinTransferFloor covers the new min_transfer_mloki floor —
// configured at the Cash Hub level, inherited by every slice a freshly
// minted wallet carries (NIP-CASH "Splitting a Slice").
func TestCashTransferMinTransferFloor(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	const minTransferMloki = happyPathAmountMloki // the floor for this hub's wallets

	req := adminCreateAppRequest{
		Name:                  ephemeralFixtureNamePrefix + " min-transfer-floor-hub",
		Scopes:                []string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE, constants.MAKE_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		Kind:                  "cash_hub",
		CashPerWalletMaxMloki: 10_000_000,
		CashMaxExpSecs:        3600,
		CashMinTransferMloki:  minTransferMloki,
	}
	resp, err := admin.createApp(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := admin.deleteApp(resp.ID); err != nil {
			t.Logf("cleanup: failed to delete ephemeral hub app_id=%d (%v)", resp.ID, err)
		}
	})
	t.Cleanup(func() {
		claims, err := admin.listCashWalletClaims(resp.ID)
		if err != nil {
			return
		}
		seen := map[uint]bool{}
		for _, claim := range claims {
			if seen[claim.WalletAppID] {
				continue
			}
			seen[claim.WalletAppID] = true
			_ = admin.deleteCashWallet(resp.ID, claim.WalletAppID)
		}
	})
	require.NoError(t, admin.transfer(nil, resp.ID, ephemeralCashHubFundLoki))
	hubClient := mustConnect(t, resp.PairingUri)

	currentPriv := newTestPrivkey(t)
	currentPub, err := nostr.GetPublicKey(currentPriv)
	require.NoError(t, err)
	const fullAmount = minTransferMloki * 10
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(currentPub, fullAmount),
		Expiry:     happyPathExpirySecs,
	}, &created))
	shared := mustConnect(t, created.PairingURI)

	t.Run("SplitAmountBelowFloor_Rejected", func(t *testing.T) {
		newPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		tooSmall := uint64(minTransferMloki - 1)
		proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "pubkey", newPub, "", tooSmall, nil, time.Now())
		var result CashTransferResult
		err = shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: currentPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
			AmountMloki:   &tooSmall,
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("RemainderBelowFloor_Rejected", func(t *testing.T) {
		newPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		// Splitting off (fullAmount - 1) would leave a remainder of 1 —
		// below the floor.
		almostAll := uint64(fullAmount - 1)
		proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "pubkey", newPub, "", almostAll, nil, time.Now())
		var result CashTransferResult
		err = shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: currentPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
			AmountMloki:   &almostAll,
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("SplitAmountAtFloor_Accepted", func(t *testing.T) {
		newPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		exactlyFloor := uint64(minTransferMloki)
		proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "pubkey", newPub, "", exactlyFloor, nil, time.Now())
		var result CashTransferResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: currentPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
			AmountMloki:   &exactlyFloor,
		}, &result))
		require.Equal(t, exactlyFloor, result.AmountMloki)
	})
}

// TestCashTransferFullTransfer_IdentityBoundTarget_StaysInPlace verifies the
// design refinement made when generalizing NIP-JW's old bearer-only spin-off
// rule for NIP-CASH: a FULL transfer to a pubkey/connection_key target is
// ALWAYS reassigned in place, unconditional on the wallet's recipient
// history — unlike a bearer target, an identity-bound transfer always
// requires a real signed proof, never just presenting a shared secret, so
// reusing the connection is safe regardless of who else has ever held it.
// Live, over the real wire — the black-box counterpart to
// nip47/controllers/cash_transfer_controller_test.go's
// TestHandleCashTransferEvent_FullTransfer_PubkeyTarget_MultiRecipientWallet_StaysInPlace.
func TestCashTransferFullTransfer_IdentityBoundTarget_StaysInPlace(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "cash-transfer-inplace-hub", nil)
	hubClient := mustConnect(t, hub.Connection)

	currentPriv := newTestPrivkey(t)
	currentPub, err := nostr.GetPublicKey(currentPriv)
	require.NoError(t, err)
	otherPriv := newTestPrivkey(t)
	otherPub, err := nostr.GetPublicKey(otherPriv)
	require.NoError(t, err)

	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: []CashWalletRecipientParam{
			{IdentityType: "pubkey", IdentityValue: currentPub, AmountMloki: happyPathAmountMloki},
			{IdentityType: "pubkey", IdentityValue: otherPub, AmountMloki: happyPathAmountMloki},
		},
		Expiry: happyPathExpirySecs,
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
	require.Empty(t, transferResult.NewWalletToken, "a pubkey-target full transfer must stay in-place, never spin off a new wallet")
	require.Nil(t, transferResult.RemainingAmountMloki, "in-place reassignment never populates remaining_amount_mloki")

	// Reassigned in place: same wallet, new identity, redeemable there. The
	// OTHER recipient's own slice must also be completely unaffected.
	newInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "inplace new identity redeem")
	newProof := buildClaimProofEvent(t, newPriv, created.WalletPubkey, newInvoice.PaymentHash, nil, time.Now())
	var newClaim ClaimFundsResult
	require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       newInvoice.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: newPub,
		IdentityEvent: eventJSON(t, newProof),
	}, &newClaim))
	require.NotEmpty(t, newClaim.Preimage)

	otherInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "inplace other recipient redeem")
	otherProof := buildClaimProofEvent(t, otherPriv, created.WalletPubkey, otherInvoice.PaymentHash, nil, time.Now())
	var otherClaim ClaimFundsResult
	require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       otherInvoice.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: otherPub,
		IdentityEvent: eventJSON(t, otherProof),
	}, &otherClaim))
	require.NotEmpty(t, otherClaim.Preimage)
}
