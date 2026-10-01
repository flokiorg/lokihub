package cashwallet

// AUDIT-D QA / D-QA-4 — the saga ROLLBACK transfers' amounts are asserted nowhere.
//
// Deps.FundInternalOverride is the seam that makes an internal transfer's amount
// observable without a bolt11 encoder. Of its four users in the tree:
//
//	consolidate_rollback_test.go:133   func(_ context.Context, fromAppID, toAppID uint, _ uint64, _ string)
//	split_in_two_rollback_test.go:42   func(_ context.Context, fromAppID, toAppID uint, _ uint64, _ string)
//	lying_reversal_test.go:49,116      uses amountMloki  (covers the FORWARD amounts)
//	auditA_qa_lnmock_amount_test.go:77 uses amountMloki  (covers Create's forward amount)
//
// The two whose entire subject is rollback are exactly the two that drop the
// amount. They assert from/to and the wallet-retention outcome, never the value.
//
// Mutation, both set to a literal zero — a rollback that returns NOTHING:
//
//	consolidate.go:153   fundInternal(ctx, deps, newApp.ID, s.WalletApp.ID, s.AmountMloki, "cash consolidate rollback")
//	                  -> fundInternal(ctx, deps, newApp.ID, s.WalletApp.ID, 0,             "cash consolidate rollback")
//	split_in_two.go:109  fundInternal(ctx, deps, carved.WalletApp.ID, params.SourceWalletApp.ID, params.CarvedAmountMloki, "cash split rollback")
//	                  -> fundInternal(ctx, deps, carved.WalletApp.ID, params.SourceWalletApp.ID, 0,                        "cash split rollback")
//
// `go vet ./cashwallet/` clean, and
// `go test ./cashwallet/ ./nip47/controllers/ ./transactions/ ./apps/ -count=1 -p 1`
// returned ok for all four. The rollback reports success, the carved/merged wallet
// is deleted, sourceFundsIntact comes back true, no CashStrandedFund is written —
// and the money never went back.
//
// For contrast, the FORWARD amounts are covered: halving consolidate.go:227 and
// create.go:925 is caught by lying_reversal_test.go, and halving create.go:707 by
// auditA_qa_lnmock_amount_test.go. Only the reversal leg is blind.
//
// bug-present: both tests below FAIL with "reversed 0 mloki, forward moved N".
// bug-absent:  both pass.

import (
	"context"
	"fmt"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

type fundCall struct {
	fromAppID, toAppID uint
	amountMloki        uint64
}

func TestAuditDQA_Consolidate_RollbackReturnsExactlyWhatTheForwardLegMoved(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	s1 := newConsolidateSourceApp(t, svc, hub, "s1", 200_000, "auditD-s1-fund")
	s2 := newConsolidateSourceApp(t, svc, hub, "s2", 200_000, "auditD-s2-fund")
	newPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	const perSource = uint64(123_000)

	var calls []fundCall
	deps := newTestDeps(svc)
	deps.FundInternalOverride = func(_ context.Context, fromAppID, toAppID uint, amountMloki uint64, _ string) error {
		calls = append(calls, fundCall{fromAppID, toAppID, amountMloki})
		switch len(calls) {
		case 1: // fund the merged wallet from s1 — succeeds
			return nil
		case 2: // fund the merged wallet from s2 — fails, starts rollback
			return fmt.Errorf("simulated forward-funding failure")
		case 3: // reverse s1's transfer — succeeds
			return nil
		default:
			t.Fatalf("unexpected 4th internal transfer call: %+v", calls)
			return nil
		}
	}

	_, strandedSourceAppIDs, err := Consolidate(context.TODO(), deps, ConsolidateParams{
		HubApp:           hub,
		Sources:          []ConsolidateSource{{WalletApp: s1, AmountMloki: perSource}, {WalletApp: s2, AmountMloki: perSource}},
		NewIdentityType:  db.CashIdentityPubkey,
		NewIdentityValue: newPk,
	})
	require.Error(t, err)
	require.Len(t, calls, 3)

	// Shape, as the existing test already checks.
	require.Equal(t, s1.ID, calls[0].fromAppID)
	require.Equal(t, s1.ID, calls[2].toAppID)

	// VALUE — the part nothing asserted.
	require.Equalf(t, perSource, calls[0].amountMloki,
		"the forward leg moved %d mloki out of s1, not the requested %d", calls[0].amountMloki, perSource)
	require.Equalf(t, calls[0].amountMloki, calls[2].amountMloki,
		"the rollback returned %d mloki to s1 but the forward leg took %d — the difference is "+
			"silently stranded, and the saga still reports the reversal as successful, deletes the "+
			"merged wallet and writes no CashStrandedFund record",
		calls[2].amountMloki, calls[0].amountMloki)

	// And the outcome the existing test asserts is reached on a reversal that
	// really did return the money, not merely on one that returned.
	require.Empty(t, strandedSourceAppIDs)
	require.Equal(t, 2, mergedChildCount(t, svc, hub))
	records, err := svc.AppsService.ListCashStrandedFunds(false)
	require.NoError(t, err)
	require.Empty(t, records)
}

func TestAuditDQA_SplitInTwo_RollbackReturnsExactlyTheCarvedAmount(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	sourceWallet := newProvenanceTestSourceWallet(t, svc, hub)

	carvedPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	remainderPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	const carved = uint64(2000)

	var calls []fundCall
	deps := newTestDeps(svc)
	deps.FundInternalOverride = func(_ context.Context, fromAppID, toAppID uint, amountMloki uint64, _ string) error {
		calls = append(calls, fundCall{fromAppID, toAppID, amountMloki})
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
		CarvedAmountMloki:      carved,
		RemainderIdentityType:  db.CashIdentityPubkey,
		RemainderIdentityValue: remainderPubkey,
		RemainderAmountMloki:   3000,
	})
	require.Error(t, err)
	require.Nil(t, result)
	require.Len(t, calls, 3)

	require.Equal(t, sourceWallet.ID, calls[0].fromAppID)
	require.Equal(t, sourceWallet.ID, calls[2].toAppID)

	require.Equalf(t, carved, calls[0].amountMloki,
		"the carved leg moved %d mloki, not the requested %d", calls[0].amountMloki, carved)
	require.Equalf(t, carved, calls[2].amountMloki,
		"the compensation returned %d mloki to the source but %d was carved out of it — "+
			"sourceFundsIntact=%v is then a lie, and the caller restores the source claim over a "+
			"balance that is short by %d",
		calls[2].amountMloki, carved, sourceFundsIntact, carved-calls[2].amountMloki)

	require.True(t, sourceFundsIntact)
}
