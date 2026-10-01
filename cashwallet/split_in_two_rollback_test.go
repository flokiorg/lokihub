package cashwallet

import (
	"context"
	"fmt"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// TestSplitInTwo_RemainderFails_CarvedReversalSucceeds_CarvedWalletDeleted is
// the package-level mirror of the controller-level
// TestHandleCashTransferEvent_PartialSplit_FailedCompensation_SourceClaimLeftInPlace
// (nip47/controllers/cash_transfer_controller_test.go): there, the carved
// wallet's reversal fails too, so it's retained. Here, the reversal SUCCEEDS,
// so the carved wallet must be deleted and sourceFundsIntact reported true —
// SplitInTwo's own "safe to restore" contract. Reached via
// Deps.FundInternalOverride (see consolidate_rollback_test.go's doc comment
// for why the equivalent real-invoice technique can't reach this specific
// branch — the same mock limitation applies here).
func TestSplitInTwo_RemainderFails_CarvedReversalSucceeds_CarvedWalletDeleted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	sourceWallet := newProvenanceTestSourceWallet(t, svc, hub)

	carvedPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	remainderPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	// amountMloki is captured, not discarded. It used to be `_ uint64`, so a
	// reversal that returned a different amount than the forward leg took — or
	// zero — passed this test unchanged (audit finding D-QA-4): the only thing
	// asserted was WHICH wallets the transfers ran between.
	type call struct {
		fromAppID, toAppID uint
		amountMloki        uint64
	}
	var calls []call
	deps := newTestDeps(svc)
	deps.FundInternalOverride = func(_ context.Context, fromAppID, toAppID uint, amountMloki uint64, _ string) error {
		calls = append(calls, call{fromAppID, toAppID, amountMloki})
		switch len(calls) {
		case 1: // fund the carved wallet from the source — succeeds
			return nil
		case 2: // fund the remainder wallet from the source — fails, starts compensation
			return fmt.Errorf("simulated remainder-funding failure")
		case 3: // reverse the carved transfer back to the source — succeeds
			return nil
		default:
			t.Fatalf("unexpected 4th internal transfer call: %+v", calls)
			return nil
		}
	}

	result, sourceFundsIntact, err := SplitInTwo(context.TODO(), deps, SplitInTwoParams{
		HubApp:                 hub,
		SourceWalletApp:        sourceWallet,
		CarvedIdentityType:     db.CashIdentityPubkey,
		CarvedIdentityValue:    carvedPubkey,
		CarvedAmountMloki:      2000,
		RemainderIdentityType:  db.CashIdentityPubkey,
		RemainderIdentityValue: remainderPubkey,
		RemainderAmountMloki:   3000,
	})
	require.Error(t, err)
	require.Nil(t, result)
	require.Len(t, calls, 3)

	assert.Equal(t, sourceWallet.ID, calls[0].fromAppID, "call 1 funds the carved wallet FROM the source")
	assert.Equal(t, sourceWallet.ID, calls[1].fromAppID, "call 2 funds the remainder wallet FROM the source")
	assert.Equal(t, sourceWallet.ID, calls[2].toAppID, "call 3 reverses the carved transfer back TO the source")
	// And for the same AMOUNT. A reversal that hands back less than it took leaves
	// the difference stranded in the carved wallet while the source's claim is
	// restored in full, so the hub's books say the money is in two places.
	assert.Equalf(t, calls[0].amountMloki, calls[2].amountMloki,
		"the carved spin-off moved %d mloki but the reversal returned %d — the difference is "+
			"stranded while the source claim is restored as if whole",
		calls[0].amountMloki, calls[2].amountMloki)
	assert.NotZerof(t, calls[2].amountMloki, "a reversal of 0 mloki is not a reversal")

	assert.True(t, sourceFundsIntact,
		"the carved transfer was successfully reversed, so it's safe for the caller to restore the source claim")

	var children []db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&children).Error)
	assert.Len(t, children, 1, "only the source remains — the carved wallet must be DELETED once its reversal succeeded")

	records, err := svc.AppsService.ListCashStrandedFunds(false)
	require.NoError(t, err)
	assert.Empty(t, records, "no reconciliation record should be written when the reversal succeeded")
}
