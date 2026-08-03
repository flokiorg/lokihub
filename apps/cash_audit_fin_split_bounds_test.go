package apps_test

// Financial/economic design review (2026-08-02) — Cash Hub / lokicash.
//
// These tests exercise the split arithmetic at the AppsService layer
// (SplitCashSliceAmount), which is the one atomic guard both the full-split
// and partial-split cash_transfer outcomes share. They do NOT create or fund
// real child wallets (that is cashwallet.Split's job) — they isolate the
// question this review cares about: what, if anything, bounds how many times a
// single funded slice can be carved up, and is value exactly conserved when it
// is.

import (
	"testing"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedSlice inserts a single unclaimed pubkey slice of amountMloki with the
// given min-transfer floor, and returns the identity value used for it.
func seedSlice(t *testing.T, svc *tests.TestService, walletID uint, amountMloki, minTransferMloki int64) string {
	t.Helper()
	idv := randomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(walletID, []db.CashWalletClaim{{
		IdentityType:     db.CashIdentityPubkey,
		IdentityValue:    idv,
		AmountMloki:      amountMloki,
		MinTransferMloki: minTransferMloki,
	}}))
	return idv
}

// countSplitsOfOne repeatedly carves 1 mloki off the slice until it is fully
// drained, returning how many split operations that took. Each partial split
// leaves the slice alive (remainder under the same identity); the final split,
// when the remaining amount is exactly 1, fully drains it. This is the exact
// sequence a rational holder does to fan a slice out into as many separate
// dedicated wallets as it has mloki.
func countSplitsOfOne(t *testing.T, svc *tests.TestService, walletID uint, idv string) int {
	t.Helper()
	splits := 0
	for {
		res, err := svc.AppsService.SplitCashSliceAmount(walletID, db.CashIdentityPubkey, idv, 1)
		if err != nil {
			t.Fatalf("unexpected split failure after %d splits: %v", splits, err)
		}
		splits++
		if res.FullyDrained {
			return splits
		}
	}
}

// FINDING (Medium): with max_transfers removed, the ONLY control that bounds
// how many child wallets / DB rows / internal-transfer ledger entries a single
// funded slice can generate through repeated splitting is min_transfer_mloki
// (bound = amount / min_transfer_mloki). Its default is 0 (no floor) — which
// makes that bound infinite. A slice of N mloki with the default floor can be
// carved into N separate pieces, one per mloki. This is the same shape as the
// removed max_transfers cap: a control framed as a dust-floor whose default
// silently fails to bound the resource/liability it is now the only thing
// standing in front of.
func TestCashAuditFin_MinTransferZero_SplitFanoutUnbounded(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCashHub(t, svc, 100000000, 3600)
	wallet := newCashWallet(t, svc, hub)

	const amount = 500

	// Default floor (0): the slice fans out into one piece per mloki — 500
	// splits from a 500-mloki slice. Nothing bounds this but the amount itself.
	idvNoFloor := seedSlice(t, svc, wallet.ID, amount, 0)
	splitsNoFloor := countSplitsOfOne(t, svc, wallet.ID, idvNoFloor)
	assert.Equal(t, amount, splitsNoFloor,
		"with min_transfer_mloki=0 (the default), a slice of N mloki permits N splits — bounded only by the amount")

	// A positive floor is the ONLY thing that re-imposes a real bound:
	// amount/floor splits, not amount splits.
	const floor = 100
	idvFloored := seedSlice(t, svc, wallet.ID, amount, floor)
	splitsFloored := 0
	remaining := int64(amount)
	for remaining > 0 {
		// Carve off exactly `floor` each time; the guard requires both the
		// piece and the remainder to be >= floor (or the remainder to be 0).
		res, err := svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, idvFloored, floor)
		require.NoError(t, err)
		splitsFloored++
		remaining = res.RemainingAmountMloki
	}
	assert.Equal(t, amount/floor, splitsFloored,
		"a positive min_transfer_mloki bounds the fan-out to amount/floor")
	assert.Less(t, splitsFloored, splitsNoFloor,
		"the default floor of 0 leaves the fan-out strictly larger (here unbounded by anything but the amount)")
}

// CONFIRMED SOUND: value is exactly conserved across an arbitrarily deep chain
// of partial splits, including odd amounts that never divide evenly. The sum
// of every carved-off piece plus the final surviving remainder always equals
// the original funded amount, with no rounding drift and no stranded dust —
// because every decrement is exact integer-mloki arithmetic (remainder =
// amount - splitMloki), never a division.
func TestCashAuditFin_ValueConservation_DeepOddChain(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCashHub(t, svc, 100000000, 3600)
	wallet := newCashWallet(t, svc, hub)

	const original = int64(100003) // deliberately prime-ish / never divides evenly
	idv := seedSlice(t, svc, wallet.ID, original, 0)

	// Carve off a lumpy, deliberately-uneven sequence of pieces, deep chain.
	pieces := []int64{1, 7, 999, 3, 50000, 2, 13, 11111, 9, 100}
	var carvedTotal int64
	remaining := original
	for _, p := range pieces {
		res, err := svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, idv, p)
		require.NoError(t, err)
		require.False(t, res.FullyDrained)
		carvedTotal += res.SplitAmountMloki
		remaining = res.RemainingAmountMloki
	}

	// Drain whatever's left in one final full split.
	res, err := svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, idv, remaining)
	require.NoError(t, err)
	require.True(t, res.FullyDrained)
	carvedTotal += res.SplitAmountMloki

	assert.Equal(t, original, carvedTotal,
		"every mloki of the original slice must land in exactly one carved-off piece — no drift, no dust, no double-count")

	// The source slice is now terminal; no further split can draw on it.
	_, err = svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, idv, 1)
	assert.ErrorIs(t, err, constants.ErrNotFound)
}
