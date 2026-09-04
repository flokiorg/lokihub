package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// TestHandleCashTransferEvent_ProofBoundToNewIdentityIAPubkey_SwappedIARejected
// is a regression test for a High-severity finding from the 2026-07-30 Cash
// Hub audit: cash_transfer's kind-23198 proof bound new_identity via
// new_identity_hash = sha256(identity_type + ":" + identity_value) — it did
// NOT include new_identity.ia_pubkey anywhere in what the caller signed.
//
// A caller who intends to transfer their slice to
// {connection_key: X, ia_pubkey: IA1} signed a proof that only committed to
// "connection_key:X". Anyone who could observe that request on the shared
// cash_wallet connection (any co-recipient, current or former, per
// NIP-CASH's own threat model) could resubmit the IDENTICAL identity_event
// with new_identity.ia_pubkey swapped to a DIFFERENT Identity Authority the
// operator also happened to trust (IA2), while keeping identity_value
// unchanged so the hash still matched — redirecting which IA is
// authoritative for a transferred/split connection_key slice, since a
// kind-35522 attestation is only honored if signed by the ia_pubkey recorded
// on the claim.
//
// Fixed by folding ia_pubkey into newIdentityHash itself
// (cash_transfer_controller.go), so a proof now commits to
// identity_type+identity_value+ia_pubkey — a swapped ia_pubkey no longer
// matches the signed hash and is rejected.
func TestHandleCashTransferEvent_ProofBoundToNewIdentityIAPubkey_SwappedIARejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	// The operator trusts TWO independent Identity Authorities.
	ia1Privkey := nostr.GeneratePrivateKey()
	ia1Pubkey, _ := nostr.GetPublicKey(ia1Privkey)
	registerTrustedIA(t, svc, ia1Pubkey)

	ia2Privkey := nostr.GeneratePrivateKey()
	ia2Pubkey, _ := nostr.GetPublicKey(ia2Privkey)
	registerTrustedIA(t, svc, ia2Pubkey)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	// The legitimate caller signs a proof for new_identity =
	// {connection_key: X, ia_pubkey: IA1} — now committing to IA1 as part of
	// the signed hash.
	targetConnectionKey := tests.RandomHex32()
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey,
		db.CashIdentityConnectionKey, targetConnectionKey, ia1Pubkey, 1000, nil, time.Now())

	// An attacker (or a race against the caller's own in-flight request)
	// resubmits the IDENTICAL proof, but swaps ia_pubkey to IA2 while leaving
	// identity_value unchanged.
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity: cashTransferNewIdentityParam{
			IdentityType:  db.CashIdentityConnectionKey,
			IdentityValue: targetConnectionKey,
			IAPubkey:      ia2Pubkey, // NOT what the caller signed for (IA1)
		},
	})

	require.NotNil(t, response.Error, "a proof signed for ia_pubkey=IA1 must not authorize a transfer targeting IA2")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	// The slice must be untouched, and no claim must exist under the swapped IA.
	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	require.NotNil(t, claim, "a rejected transfer must not have touched the slice")
	swappedClaim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityConnectionKey, targetConnectionKey)
	require.NoError(t, err)
	assert.Nil(t, swappedClaim, "the slice must not have been registered against the swapped IA")

	// Sanity: the SAME proof, resubmitted with the ORIGINAL ia_pubkey (IA1),
	// must still succeed — the fix binds to whatever ia_pubkey was actually
	// signed for, not a blanket rejection of connection_key targets.
	okResponse := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity: cashTransferNewIdentityParam{
			IdentityType:  db.CashIdentityConnectionKey,
			IdentityValue: targetConnectionKey,
			IAPubkey:      ia1Pubkey,
		},
	})
	require.Nil(t, okResponse.Error, "the same proof against the IA it was actually signed for must still succeed")
}
