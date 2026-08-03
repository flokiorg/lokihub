package apps_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// TestUndoCashSliceSplit_ClaimedFirst_ReturnsNotFoundInsteadOfSilentNoOp is a
// regression test for a Low-severity finding from the 2026-07-30 Cash Hub
// audit: when cash_transfer's split path fails AFTER SplitCashSliceAmount's
// atomic decrement has already committed (e.g. the new spun-off wallet's own
// creation or internal funding transfer subsequently errors — see
// cashwallet.Split and handleCashTransferSplit's rollback), the controller
// calls AppsService.UndoCashSliceSplit to restore the source slice's
// AmountMloki. That call is guarded by "claimed_at IS NULL" (mirroring every
// other atomic guard in this file), which is the RIGHT thing to do if the
// row has since been legitimately claimed (redeemed, transferred, or fully
// split by someone else) — but UndoCashSliceSplit used to not check
// RowsAffected, so this failure mode was entirely silent: it returned nil
// whether or not it actually restored anything, stranding the carved-off
// amount with no matching CashWalletClaim row anywhere (the real ledger
// balance stays untouched — the internal transfer that would have consumed
// it is exactly what failed — but the operator has no visibility into the
// discrepancy until the periodic expiry sweep eventually reclaims it).
//
// Fixed by having UndoCashSliceSplit return an error wrapping
// constants.ErrNotFound when RowsAffected == 0, matching every sibling
// atomic method's "lost the race" convention, and having
// handleCashTransferSplit's rollback log that case at Error level with the
// wallet/identity/amount an operator needs to locate and manually sweep it.
func TestUndoCashSliceSplit_ClaimedFirst_ReturnsNotFoundInsteadOfSilentNoOp(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCashHub(t, svc, 10_000, 3600)
	wallet := newCashWallet(t, svc, hub)
	pubkey := randomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pubkey, AmountMloki: 5000},
	}))

	// Step 1: cash_transfer's split path atomically carves 2000 off, exactly
	// as SplitCashSliceAmount does before cashwallet.Split ever runs.
	result, err := svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, pubkey, 2000)
	require.NoError(t, err)
	require.False(t, result.FullyDrained)
	require.EqualValues(t, 3000, result.RemainingAmountMloki)

	// Step 2: BEFORE the split's own rollback runs (e.g. cashwallet.Split's
	// new-wallet creation/funding is still failing out on the network), a
	// perfectly legitimate concurrent operation claims the row for its
	// current, already-shrunk amount (3000) — e.g. a cash_redeem for the
	// caller's own remaining balance.
	claimedAmount, err := svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, pubkey)
	require.NoError(t, err)
	assert.EqualValues(t, 3000, claimedAmount)

	// Step 3: cashwallet.Split's funding transfer now fails (simulated: we
	// never called cashwallet.Split at all, standing in for a real LN/DB
	// error), so handleCashTransferSplit's rollback fires and calls
	// UndoCashSliceSplit to put the 2000 back.
	err = svc.AppsService.UndoCashSliceSplit(wallet.ID, db.CashIdentityPubkey, pubkey, 2000)

	// FIXED: an error is now returned, distinguishable via ErrNotFound, so
	// the caller (handleCashTransferSplit's rollback) can log it loudly
	// instead of silently swallowing the discrepancy.
	require.Error(t, err, "UndoCashSliceSplit must report that it changed nothing")
	assert.True(t, errors.Is(err, constants.ErrNotFound), "must be distinguishable as a race/already-claimed condition, not a generic DB error")

	// The claimed row's own bookkeeping is untouched and correct — this part
	// was never buggy, just the silent-success return value.
	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, pubkey)
	require.NoError(t, err)
	assert.Nil(t, claim, "the slice is claimed/terminal now — GetCashWalletClaim correctly no longer finds it unclaimed")

	all, err := svc.AppsService.ListClaimsForWallet(wallet.ID)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.EqualValues(t, 3000, all[0].AmountMloki,
		"the claimed row's own AmountMloki is (correctly) still 3000, exactly what cash_redeem paid out")
}
