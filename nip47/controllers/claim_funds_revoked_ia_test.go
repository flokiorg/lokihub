package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// TestHandleClaimFundsEvent_ConnectionKeyMode_RevokedIA_Rejected is a
// regression test for HandleClaimFundsEvent's step 7: an Identity Authority
// that was trusted when a jit_wallet was created (honestly, or having
// colluded with an attacker to attest a false connection_key->pubkey
// binding) and is later revoked by the operator must have that revocation
// take effect immediately, even though the attestation it already issued
// remains cryptographically valid and unexpired. Before the fix,
// apps.IdentityAuthorityManager.IsTrusted was only ever consulted once, at
// wallet-creation time (jitwallet.Resolve) - never re-checked at claim time -
// so a revoked IA's still-unexpired attestation kept authorizing payouts
// until the attestation's own expiration lapsed.
func TestHandleClaimFundsEvent_ConnectionKeyMode_RevokedIA_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	iaManager := apps.NewIdentityAuthorityManager(svc.DB)
	iaPrivkey := nostr.GeneratePrivateKey()
	iaPubkey, _ := nostr.GetPublicKey(iaPrivkey)
	_, err = iaManager.Add(iaPubkey, "compromised-ia", nil)
	require.NoError(t, err)

	connectionKey := tests.RandomHex32()
	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)

	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityConnectionKey, IdentityValue: connectionKey, IAPubkey: iaPubkey, AmountMloki: 1000},
	}))

	attestation := buildIAAttestationEvent(t, iaPrivkey, connectionKey, claimantPubkey, oneHourFromNow())
	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash,
		nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())

	// The operator discovers the IA is compromised/malicious and revokes it -
	// this must immediately block any future claim_funds call relying on its
	// attestations, regardless of the attestation's own (still valid)
	// expiration.
	require.NoError(t, iaManager.Delete(iaPubkey))
	trusted, err := iaManager.IsTrusted(iaPubkey)
	require.NoError(t, err)
	require.False(t, trusted, "sanity check: the IA is in fact no longer trusted")

	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, claimFundsParams{
		Invoice:          tests.MockZeroAmountInvoice,
		Amount:           ptrUint64(1000),
		IdentityType:     db.JITAllocIdentityConnectionKey,
		IdentityValue:    connectionKey,
		IdentityEvent:    mustMarshal(t, proof),
		AttestationEvent: mustMarshal(t, attestation),
	})

	require.NotNil(t, response.Error, "a claim attested by a revoked Identity Authority must be rejected")
	assert.Equal(t, constants.ERROR_RESTRICTED, response.Error.Code)

	// A rejected claim due to IA revocation must not burn the recipient's
	// underlying entitlement - the slice must remain claimable (e.g. once the
	// operator re-registers a corrected IA and re-attests it).
	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityConnectionKey, connectionKey)
	require.NoError(t, err)
	require.NotNil(t, claim, "a revoked-IA rejection must not consume the slice")
}
