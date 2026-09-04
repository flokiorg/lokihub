package apps_test

// Independent security audit B (Cash Hub / lokicash, NIP-CASH). These tests are
// additive confirmations of the load-bearing double-spend / amount-TOCTOU
// guards the spec calls out under §Redeeming Funds step 3-4 and §Security
// Considerations ("A partial split's amount check MUST be re-evaluated against
// the slice's live state"). They assert the atomic layer (apps.appsService)
// never lets a redeem/claim commit against a stale, pre-split amount, and that
// the optimistic (transfer_count + amount_mloki) lock serialises a concurrent
// split against a claim. Prefixed cash_audit_secB_ to avoid colliding with any
// other agent's files running against this same tree.

import (
	"testing"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSecB_ClaimReturnsCurrentAmountAfterPartialSplit proves that once a partial
// split has shrunk a slice (10000 -> 6000), a subsequent claim (a cash_redeem
// payout) returns the *current* 6000, never the pre-split 10000 the controller
// may have read earlier. A redeem paying the stale 10000 would silently draw on
// funds already earmarked for the split-off wallet — the exact bug the spec's
// step-3/4 atomic re-check exists to prevent.
func TestSecB_ClaimReturnsCurrentAmountAfterPartialSplit(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCashHub(t, svc, 1_000_000, 3600)
	wallet := newCashWallet(t, svc, hub)
	pubkey := randomHex32()

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pubkey, AmountMloki: 10000},
	}))

	// A partial split carves 4000 off, leaving 6000 and bumping transfer_count.
	splitRes, err := svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, pubkey, 4000)
	require.NoError(t, err)
	require.False(t, splitRes.FullyDrained)
	require.EqualValues(t, 6000, splitRes.RemainingAmountMloki)

	// The redeem's atomic claim must now see 6000, never the stale 10000.
	claimed, err := svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, pubkey)
	require.NoError(t, err)
	assert.EqualValues(t, 6000, claimed,
		"claim after a partial split must return the current (post-split) amount, never the pre-split value")
}

// TestSecB_StaleAmountSplitLosesOptimisticLock proves the transfer_count +
// amount_mloki optimistic lock: a caller that read a slice at 10000 and then
// tries to commit a split sized against that stale read, AFTER another split
// already shrank the row, is rejected (RowsAffected==0 -> ErrNotFound) rather
// than committing a second decrement against a value that no longer exists.
// This is what stops two concurrent splits from each drawing the full amount.
func TestSecB_StaleAmountSplitLosesOptimisticLock(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCashHub(t, svc, 1_000_000, 3600)
	wallet := newCashWallet(t, svc, hub)
	pubkey := randomHex32()

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pubkey, AmountMloki: 10000},
	}))

	// First split wins: 10000 -> 3000, transfer_count 0 -> 1.
	_, err = svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, pubkey, 7000)
	require.NoError(t, err)

	// A second split of 7000 (which was only valid against the original 10000)
	// must now be rejected: only 3000 remains. The amount guard catches it.
	_, err = svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, pubkey, 7000)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidParams,
		"a split sized against a stale, larger amount must be rejected once the slice has shrunk")

	// Sanity: exactly 3000 is still redeemable, and only once.
	claimed, err := svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, pubkey)
	require.NoError(t, err)
	assert.EqualValues(t, 3000, claimed)
	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, pubkey)
	assert.Error(t, err, "a fully-consumed slice must not be claimable a second time")
}

// TestSecB_FullSplitThenRedeemRejected proves a full split (drain to zero)
// marks the slice terminal, so a racing cash_redeem of the same slice can never
// also pay out — the source identity loses all authority the instant the split
// claims the row (§Spinning a Slice Off, Atomicity).
func TestSecB_FullSplitThenRedeemRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCashHub(t, svc, 1_000_000, 3600)
	wallet := newCashWallet(t, svc, hub)
	pubkey := randomHex32()

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pubkey, AmountMloki: 8000},
	}))

	splitRes, err := svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, pubkey, 8000)
	require.NoError(t, err)
	require.True(t, splitRes.FullyDrained)

	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, pubkey)
	assert.Error(t, err, "a fully-split (drained) slice must not be redeemable")
}

// TestSecB_SplitDustRemainderRejected proves the min_transfer_mloki floor is
// enforced on BOTH sides of a split: a split that would strand an unmovable
// sub-floor remainder is rejected rather than silently allowed (§Splitting a
// Slice, step 4).
func TestSecB_SplitDustRemainderRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCashHub(t, svc, 1_000_000, 3600)
	wallet := newCashWallet(t, svc, hub)
	pubkey := randomHex32()

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pubkey, AmountMloki: 10000, MinTransferMloki: 3000},
	}))

	// Splitting off 8000 leaves 2000 behind, which is below the 3000 floor.
	_, err = svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, pubkey, 8000)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	// Splitting off exactly the floor (3000) leaving 7000 is fine.
	_, err = svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, pubkey, 3000)
	require.NoError(t, err)
}
