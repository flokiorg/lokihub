package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// TestAuditA_SecB_CashModeAfterInPlaceHandoff_MustSplit closes the hole Session A's
// second security auditor found: the cash-mode eligibility gate was blind to an
// in-place identity HANDOFF, as distinct from a recipient REMOVAL.
//
// The rule in NIP-CASH §Cash-mode targets is that a wallet may go cash-mode in place
// only if it has, and has ALWAYS had, exactly one recipient. The gate approximated
// "always had" as "one live claim AND no archive row", on the stated reasoning that
// live rows and archive rows are disjoint and their union is the lifetime set.
//
// That union is not the lifetime set after a handoff. ReassignCashSliceIdentity
// rewrites a claim's identity in place and writes NO archive row, so a wallet that
// has changed hands looks identical to one that never left its first owner.
//
// Why that costs money. The bill's pairing secret is derived from its app ID
// (cashwallet/create.go:549) and therefore cannot be rotated, so Alice still holds a
// working connection to this wallet after transferring it away. A cash redemption
// carries its raw cash_secret in the request body. So reassigning to cash mode in
// place publishes a bearer secret on a connection a previous owner reads — and, the
// single-winner ClaimCashSlice being a race, she can spend it first and recover a bill
// she already sold.
//
// The fix reads claim.TransferCount, which is an exact count of those handoffs, and
// forces the split path. This test pins the SECURITY property rather than the
// mechanism: the cash slice must not be redeemable on the wallet whose connection a
// previous owner still holds.
//
// Against the pre-fix code this fails at the final assertion — the cash claim appears
// on the original wallet.
func TestAuditA_SecB_CashModeAfterInPlaceHandoff_MustSplit(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	// Alice is the wallet's sole recipient, and will remain able to reach this
	// connection forever: the pairing key is derived from the app ID.
	alicePriv := nostr.GeneratePrivateKey()
	alicePub, _ := nostr.GetPublicKey(alicePriv)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: alicePub, AmountMloki: 1000},
	}))

	// Step 1 — Alice sells the bill to Bob. pubkey -> pubkey, full amount. This
	// SHOULD reassign in place: a pubkey redemption still needs Bob's own signature,
	// so nothing bearer is exposed and there is no reason to spend a new wallet.
	bobPriv := nostr.GeneratePrivateKey()
	bobPub, _ := nostr.GetPublicKey(bobPriv)
	aliceProof := buildTransferProofEvent(t, alicePriv, *wallet.WalletPubkey, db.CashIdentityPubkey, bobPub, "", 1000, nil, time.Now())

	handoff := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: alicePub,
		IdentityEvent: mustMarshal(t, aliceProof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: bobPub},
	})
	require.Nil(t, handoff.Error)
	handoffResult := handoff.Result.(cashTransferResponse)
	require.Empty(t, handoffResult.NewWalletPubkey,
		"a pubkey-to-pubkey full transfer is expected to reassign in place; if this became a split the test's premise is gone")

	// The handoff is now recorded ONLY in TransferCount — there is no archive row,
	// which is exactly why the gate could not see it.
	bobClaim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, bobPub)
	require.NoError(t, err)
	require.NotNil(t, bobClaim)
	require.Positive(t, bobClaim.TransferCount, "the in-place handoff must have advanced TransferCount")
	archived, err := svc.AppsService.HasArchivedSliceForWallet(wallet.ID)
	require.NoError(t, err)
	require.False(t, archived,
		"an in-place handoff writes no archive row — this is the blind spot, and if it ever starts writing one this test stops testing the gap")

	// Step 2 — Bob converts to cash mode, full amount. The gate sees one live claim
	// and no archive row. It must NOT conclude "always solo".
	newSecretHex, newSecretHash := cashSecretAndHash(t)
	bobProof := buildTransferProofEvent(t, bobPriv, *wallet.WalletPubkey, db.CashIdentityCash, newSecretHash, "", 1000, nil, time.Now())

	toCash := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: bobPub,
		IdentityEvent: mustMarshal(t, bobProof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityCash, IdentityValue: newSecretHash},
	})
	// What this test can and cannot observe. The property under test is the ROUTING
	// DECISION — in place versus carve into a new wallet — and that is fully
	// observable here. Whether the carve then FUNDS is not: the controller builds its
	// cashwallet.Deps inline (cash_transfer_controller.go:733), so there is no seam to
	// pass Deps.FundInternalOverride from a controller test, and the mock LN client
	// cannot complete the internal transfer. So a post-fix run legitimately ends in
	// "failed to split off slice" while a pre-fix run succeeds in place.
	//
	// That difference is exactly the discrimination we want, and the assertion below is
	// written on the claim table rather than on the response for that reason: after the
	// fix the bearer slice is absent from the original wallet whether or not the carve
	// completed, and before the fix it is present. Completing the carve end to end is
	// integration-level and belongs with the live suite.
	if toCash.Error != nil {
		t.Logf("carve attempted and did not complete in this harness (expected): %s: %s",
			toCash.Error.Code, toCash.Error.Message)
	}

	// The security property, stated directly: the bearer slice must not live on the
	// wallet Alice can still reach.
	strandedOnAlicesConnection, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityCash, newSecretHash)
	require.NoError(t, err)
	assert.Nil(t, strandedOnAlicesConnection,
		"the cash slice was reassigned IN PLACE onto the original wallet — Alice, the previous owner, still holds a working "+
			"connection to it (the pairing key is derived from the app ID and cannot be rotated), so she reads Bob's raw "+
			"cash_secret off the wire at redemption and can front-run the single-winner ClaimCashSlice to recover a bill she already sold")

	// And the positive half, to the extent this harness can see it: if the carve DID
	// complete, it must have landed on a wallet other than the original — whose pairing
	// secret derives from an app ID no previous owner has.
	if toCash.Error == nil {
		cashResult := toCash.Result.(cashTransferResponse)
		assert.NotEmpty(t, cashResult.NewWalletPubkey,
			"a cash-mode conversion on a wallet that has changed hands must carve into a NEW wallet")
		assert.NotEqual(t, *wallet.WalletPubkey, cashResult.NewWalletPubkey)
	}
	assert.NotEmpty(t, newSecretHex)
}
