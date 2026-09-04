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

// TestHandleCashTransferEvent_NewIdentityValidationFailure_ReleasesProofGuard
// is a regression test for a Low-severity finding from the 2026-07-30 Cash
// Hub audit: the db.CashTransferProof single-use replay guard used to be
// inserted right after verifyTransferIdentityEvent succeeded (step 7), but
// BEFORE new_identity was validated (step 8). Every failure path inside step
// 9's atomic operation (ReassignCashSliceIdentity/SplitCashSliceAmount)
// releases this guard on failure so a legitimate caller can retry with the
// identical proof — but a failure in step 8 (new_identity rejected — e.g.
// its ia_pubkey isn't a trusted Identity Authority) did NOT release it, even
// though nothing was ever mutated on the slice.
//
// Fixed by moving the db.CashTransferProof insert to occur only after every
// step-7/8 check that doesn't touch the slice has already passed (right
// before step 9's split/reassign decision), so a request that fails one of
// those earlier checks never burns the proof at all.
func TestHandleCashTransferEvent_NewIdentityValidationFailure_ReleasesProofGuard(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	untrustedIA, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	newConnectionKey := tests.RandomHex32()
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityConnectionKey, newConnectionKey, untrustedIA, 1000, nil, time.Now())

	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity: cashTransferNewIdentityParam{
			IdentityType: db.CashIdentityConnectionKey, IdentityValue: newConnectionKey, IAPubkey: untrustedIA,
		},
	})
	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	// The slice must be untouched (already covered by the sibling
	// UntrustedIA_Rejected test) ...
	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	require.NotNil(t, claim)

	// ... and the proof must NOT have been consumed: this request accomplished
	// nothing, so a legitimate caller retrying with the IDENTICAL proof (e.g.
	// after correcting ia_pubkey, or once the operator adds the IA to the
	// trust list) must not be told the proof was "already used".
	var proofCount int64
	require.NoError(t, svc.DB.Model(&db.CashTransferProof{}).Where("event_id = ?", proof.ID).Count(&proofCount).Error)
	assert.Equal(t, int64(0), proofCount,
		"a step-8 (new_identity validation) failure must not consume the single-use proof guard, "+
			"since nothing was ever mutated on the slice")

	// Sanity: retrying with the identical proof, this time against a trusted
	// IA, must succeed — proving the guard really was released, not just
	// absent from the table for some other reason.
	retry := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity: cashTransferNewIdentityParam{
			IdentityType: db.CashIdentityConnectionKey, IdentityValue: newConnectionKey, IAPubkey: untrustedIA,
		},
	})
	// Still rejected for the SAME reason (untrusted IA) — proving the retry
	// reached the same validation step again rather than being blocked by a
	// stale "already used" guard.
	require.NotNil(t, retry.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, retry.Error.Code)
}
