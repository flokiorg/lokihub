package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Security Considerations: with no per-attestation revocation, an
// attestation's expiration is its only bound, so one with no expiration tag, or
// one already past, MUST be rejected (CASH-security-considerations-15). Every
// other test attestation carries a future expiration, so these are the only
// ones that reach the checks.

func attestationFor(t *testing.T, expiration *int64) (ev *nostr.Event, iaPubkey, claimant, connectionKey string) {
	t.Helper()
	iaPrivkey := nostr.GeneratePrivateKey()
	iaPubkey, _ = nostr.GetPublicKey(iaPrivkey)
	claimant, _ = nostr.GetPublicKey(nostr.GeneratePrivateKey())
	connectionKey = tests.RandomHex32()
	return buildIAAttestationEvent(t, iaPrivkey, connectionKey, claimant, expiration), iaPubkey, claimant, connectionKey
}

func TestVerifyClaimAttestationEvent_UnexpiredAccepted(t *testing.T) {
	ev, ia, claimant, key := attestationFor(t, oneHourFromNow())
	require.NoError(t, VerifyClaimAttestationEvent(ev, ia, claimant, key))
}

func TestVerifyClaimAttestationEvent_NoExpirationRejected(t *testing.T) {
	ev, ia, claimant, key := attestationFor(t, nil)
	err := VerifyClaimAttestationEvent(ev, ia, claimant, key)
	require.Error(t, err, "an attestation that never expires must not be honoured")
	assert.Contains(t, err.Error(), "expiration")
}

func TestVerifyClaimAttestationEvent_ExpiredRejected(t *testing.T) {
	past := time.Now().Add(-time.Minute).Unix()
	ev, ia, claimant, key := attestationFor(t, &past)
	require.Error(t, VerifyClaimAttestationEvent(ev, ia, claimant, key), "an expired attestation must not be honoured")
}
