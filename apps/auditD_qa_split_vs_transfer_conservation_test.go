package apps_test

// AUDIT-D QA / D-QA-2 — the conservation check that
// TestSplitAndReassignCashSliceIdentity_PartialVsTransfer_NeverBothSucceed
// (cash_hub_service_test.go:1134) promises in a comment and then does not perform.
//
// That test races a PARTIAL split against an in-place transfer of the same slice.
// When both report success it reaches:
//
//	// What must NEVER happen is silent overdraw: verify the slice's final
//	// state is consistent with exactly one full end-to-end interleaving,
//	// not a lost update.
//	t.Skip("both succeeded via internal serialization ...")
//
// No verification follows the comment, and t.Skip inside the trial loop abandons
// the remaining trials as well. So the one outcome the test exists to judge is the
// one outcome it declines to judge, and `go test` reports SKIP, which is `ok`.
//
// This file asserts the invariant instead of skipping it:
//
//   - value in  == value out: whatever was carved off, plus whatever the slice
//     still holds, must equal what the slice held before the race;
//   - a transfer that reported success must have moved exactly the amount it
//     reported, to the identity it reported it to.
//
// Demonstrated with this mutation to apps/cash_hub_service.go:477 (the partial
// branch's optimistic lock), which compiles and vets clean:
//
//	-Where("id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NULL AND transfer_count = ? AND amount_mloki = ?",
//	-     claim.ID, identityType, identityValue, claim.TransferCount, claim.AmountMloki).
//	+Where("id = ? AND claimed_at IS NULL AND amount_mloki = ?",
//	+     claim.ID, claim.AmountMloki).
//
// It leaves the split-vs-split guard intact (amount_mloki is still pinned, so
// TestSplitCashSliceAmount_ConcurrentPartialSplits_NeverOverdraw still passes) and
// drops only the pins that stop a split committing across a concurrent transfer.
//
// bug-present: `go test ./apps/ -count=1` -> ok, with
//              TestSplitAndReassign...PartialVsTransfer_NeverBothSucceed SKIPped;
//              this test FAILS, reporting the transfer was told 5000 moved while
//              the slice holds 3000 and 2000 was carved elsewhere.
// bug-absent:  this test passes; exactly one of the two operations wins.

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestAuditDQA_PartialSplitVsTransfer_ConservesTheSliceAmount(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const (
		trials      = 200
		sliceAmount = int64(5000)
		carve       = int64(2000)
	)

	bothWon := 0
	for trial := 0; trial < trials; trial++ {
		hub := newCashHub(t, svc, 1_000_000, 3600)
		wallet := newCashWallet(t, svc, hub)
		pubkey := randomHex32()
		newPubkey := randomHex32()
		require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
			{IdentityType: db.CashIdentityPubkey, IdentityValue: pubkey, AmountMloki: sliceAmount},
		}))

		ready := make(chan struct{})
		var wg sync.WaitGroup
		var splitErr, transferErr error
		var splitRes apps.CashSliceSplitResult
		var transferred int64
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-ready
			splitRes, splitErr = svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, pubkey, carve)
		}()
		go func() {
			defer wg.Done()
			<-ready
			transferred, transferErr = svc.AppsService.ReassignCashSliceIdentity(
				wallet.ID, db.CashIdentityPubkey, pubkey, db.CashIdentityPubkey, newPubkey, "")
		}()
		close(ready)
		wg.Wait()

		splitWon := splitErr == nil
		transferWon := transferErr == nil
		require.Truef(t, splitWon || transferWon,
			"trial %d: neither op succeeded (splitErr=%v transferErr=%v)", trial, splitErr, transferErr)

		// What the row holds now, under whichever identity still owns it.
		var live []db.CashWalletClaim
		require.NoError(t, svc.DB.Where("wallet_app_id = ? AND claimed_at IS NULL", wallet.ID).Find(&live).Error)

		var stillHeld int64
		for _, c := range live {
			stillHeld += c.AmountMloki
		}
		carvedOff := int64(0)
		if splitWon {
			carvedOff = splitRes.SplitAmountMloki
		}

		// INVARIANT 1 — conservation. Nothing may be created; a full drain
		// (stillHeld == 0) is the legitimate terminal case.
		require.Equalf(t, sliceAmount, stillHeld+carvedOff,
			"trial %d: OVERDRAW — the slice held %d, but %d is still claimable and %d was carved off "+
				"(split won=%v, transfer won=%v). %d mloki was created out of a lost update.",
			trial, sliceAmount, stillHeld, carvedOff, splitWon, transferWon,
			stillHeld+carvedOff-sliceAmount)

		// INVARIANT 2 — a transfer that reported success moved exactly what it
		// said, to the identity it said. This is the half the skipped branch
		// never reaches: ReassignCashSliceIdentity returns the PRE-race amount
		// it read, so a concurrent split that still commits makes it a lie.
		if transferWon {
			claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, newPubkey)
			require.NoErrorf(t, err, "trial %d", trial)
			require.NotNilf(t, claim,
				"trial %d: a winning transfer must leave its new identity claimable", trial)
			require.Equalf(t, transferred, claim.AmountMloki,
				"trial %d: the transfer reported %d mloki moved to the new identity, which actually "+
					"holds %d — a concurrent split shrank the slice after the transfer read it",
				trial, transferred, claim.AmountMloki)
		}

		if splitWon && transferWon {
			bothWon++
		}
	}

	// Recorded, not asserted: both-succeed is a legitimate serialization outcome
	// at this bare service layer. The point of this file is that when it happens
	// the invariants above are still checked rather than skipped.
	t.Logf("AUDITD-QA-2: %d/%d trials had both operations report success; "+
		"conservation and transfer-amount honesty asserted on every one of them", bothWon, trials)
}
