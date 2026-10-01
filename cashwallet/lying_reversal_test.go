package cashwallet

// Session B's B-3: the compensating sagas deleted their wallet on the strength of the
// reversal CALL returning nil, which is a weaker statement than "the wallet is empty".
//
// The two existing rollback tests cover the reversal succeeding and the reversal
// erroring. Neither covers the third case, which is the one B-3 described: the reversal
// reports success and the money is still there. DeleteCashBill then archives a funded
// wallet, the source claim is reported safe to restore, and the same value exists in
// two places — the archived wallet and the restored claim.
//
// B-3's trigger was never confirmed as holder-inducible and this does not invent one.
// What it pins is that the delete now rests on the LEDGER rather than on inferred
// control flow, which is cheap enough not to need a trigger.
//
// The lie is built with Deps.FundInternalOverride: a forward transfer really credits
// the destination (tests.FundApp writes the settled incoming row a real internal
// transfer would), and the reversal returns nil without moving anything.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
)

func TestConsolidate_ReversalReportsSuccessButMoneyStays_MergedWalletRetained(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	s1 := newConsolidateSourceApp(t, svc, hub, "s1", 200_000, "lying-s1-fund")
	s2 := newConsolidateSourceApp(t, svc, hub, "s2", 200_000, "lying-s2-fund")
	newPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	const contribution = uint64(123_000)
	var forwardCalls, reversalCalls int

	deps := newTestDeps(svc)
	deps.FundInternalOverride = func(_ context.Context, fromAppID, toAppID uint, amountMloki uint64, memo string) error {
		if strings.Contains(memo, "rollback") {
			reversalCalls++
			// THE LIE: reports success, moves nothing. A real fundInternal that
			// settled its outgoing leg but never credited the incoming one looks
			// exactly like this from here.
			return nil
		}
		forwardCalls++
		if forwardCalls == 2 {
			return fmt.Errorf("simulated forward-funding failure on the second source")
		}
		// A real forward transfer: the destination is genuinely credited.
		tests.FundApp(svc, toAppID, amountMloki, fmt.Sprintf("lying-forward-%d", forwardCalls))
		return nil
	}

	_, strandedSourceAppIDs, err := Consolidate(context.TODO(), deps, ConsolidateParams{
		HubApp:           hub,
		Sources:          []ConsolidateSource{{WalletApp: s1, AmountMloki: contribution}, {WalletApp: s2, AmountMloki: contribution}},
		NewIdentityType:  db.CashIdentityPubkey,
		NewIdentityValue: newPk,
	})
	require.Error(t, err, "the consolidate itself must still fail")
	require.Equal(t, 1, reversalCalls, "s1's transfer is the only one to reverse")

	// The merged wallet must SURVIVE. Pre-fix it was deleted, because the reversal
	// returned nil — and s1's 123,000 mloki went with it.
	assert.Equal(t, 3, mergedChildCount(t, svc, hub),
		"the merged wallet was deleted while still holding funds: archiving it erases "+
			"the only record of where they are (s1 and s2 plus the retained merged wallet = 3)")

	// And it must still hold the money, which is the whole reason not to delete it.
	var merged db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ? AND id NOT IN ?",
		hub.ID, db.AppKindCashWallet, []uint{s1.ID, s2.ID}).First(&merged).Error)
	assert.Equal(t, int64(contribution), queries.GetIsolatedBalance(svc.DB, merged.ID),
		"the retained wallet must still show the funds the reversal claimed to have moved")

	// The source must NOT be reported safe to restore. Restoring it while the value
	// sits in the merged wallet would make the same money claimable twice.
	assert.Contains(t, strandedSourceAppIDs, s1.ID,
		"s1 was reported safe to restore while its contribution is still in the merged wallet")

	// And an operator can find it by query rather than by grepping logs.
	records, err := svc.AppsService.ListCashStrandedFunds(false)
	require.NoError(t, err)
	require.Len(t, records, 1, "a reconciliation record must be written")
	assert.Equal(t, "consolidate", records[0].Operation)
	assert.Equal(t, merged.ID, records[0].RetainedWalletAppID)
	assert.Equal(t, contribution, records[0].AmountMloki)
}

func TestSplitInTwo_ReversalReportsSuccessButMoneyStays_CarvedWalletRetained(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	source := newConsolidateSourceApp(t, svc, hub, "split-source", 200_000, "lying-split-fund")
	carvedPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	remainderPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	const carvedAmount = uint64(50_000)
	var forwardCalls, reversalCalls int

	deps := newTestDeps(svc)
	deps.FundInternalOverride = func(_ context.Context, fromAppID, toAppID uint, amountMloki uint64, memo string) error {
		if strings.Contains(memo, "rollback") {
			reversalCalls++
			return nil // the same lie
		}
		forwardCalls++
		if forwardCalls == 2 {
			// The remainder spin-off fails, which is what drives the rollback.
			return fmt.Errorf("simulated remainder failure")
		}
		tests.FundApp(svc, toAppID, amountMloki, fmt.Sprintf("lying-split-forward-%d", forwardCalls))
		return nil
	}

	_, sourceSafeToRestore, err := SplitInTwo(context.TODO(), deps, SplitInTwoParams{
		HubApp:            hub,
		SourceWalletApp:   source,
		CarvedAmountMloki: carvedAmount,
		// Required, or SplitInTwo returns early with no remainder to fail and so no
		// rollback at all — which is what the first version of this test hit.
		RemainderAmountMloki:   150_000,
		CarvedIdentityType:     db.CashIdentityPubkey,
		CarvedIdentityValue:    carvedPk,
		RemainderIdentityType:  db.CashIdentityPubkey,
		RemainderIdentityValue: remainderPk,
	})
	require.Error(t, err)
	require.Equal(t, 1, reversalCalls)

	assert.False(t, sourceSafeToRestore,
		"the source was reported safe to restore while the carved wallet still holds "+
			"its funds — the same value would be claimable twice")

	var carved db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ? AND id != ?",
		hub.ID, db.AppKindCashWallet, source.ID).First(&carved).Error,
		"the carved wallet must survive, as the only record of where the funds are")
	assert.Equal(t, int64(carvedAmount), queries.GetIsolatedBalance(svc.DB, carved.ID))

	records, err := svc.AppsService.ListCashStrandedFunds(false)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "split", records[0].Operation)
	assert.Equal(t, source.ID, records[0].SourceWalletAppID)
	assert.Equal(t, carved.ID, records[0].RetainedWalletAppID)
}
