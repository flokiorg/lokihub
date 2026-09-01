package cashwallet

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// TestConsolidate_FailedReversal_StrandsSourceFundsInRetainedWallet is Auditor
// B's independent, source-only substantiation of the fund-side precondition
// behind their finding 1: after a mid-loop + failed-reversal Consolidate,
// source 1's funds have LEFT source 1 (its outgoing leg settled) and were
// NEVER returned (the reversal failed), so s1's real balance is short by the
// moved amount. Auditor B's full finding went further, at the controller
// layer this cashwallet-package test doesn't reach: the controller's
// unclaimAll() was restoring every claimed source's slice to its full
// original amount unconditionally, even for a source in exactly this
// situation — creating a double-entitlement (the restored claim plus the
// merged wallet's retained funds both backing the same money). That half is
// now fixed: Consolidate returns strandedSourceAppIDs, and
// nip47/controllers/cash_consolidate_controller.go's unclaimAll skips
// restoring any source it names — see
// TestConsolidate_StrandedSource_ClaimLeftInPlace in
// nip47/controllers/cash_consolidate_guards_test.go for the controller-level
// regression. This test remains valuable as the independent, cashwallet-layer
// proof of the precondition that fix depends on.
//
// It reaches the failure the same real way the author's own regression test
// does (replaying tests.MockInvoice's payment_hash, which the transactions
// service's genuine "already paid" guard rejects) — no fault-injection seam.
func TestConsolidate_FailedReversal_StrandsSourceFundsInRetainedWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	s1 := newConsolidateSourceApp(t, svc, hub, "s1", 200_000, "s1-fund")
	s2 := newConsolidateSourceApp(t, svc, hub, "s2", 200_000, "s2-fund")
	newPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	const moved = uint64(123_000)

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: int64(moved)}, // 1: s1 -> merged: succeeds, settles MockInvoice's hash
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p2", Amount: int64(moved)}, // 2: s2 -> merged: same hash, fails -> begins rollback
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p3", Amount: int64(moved)}, // 3: reversal merged -> s1: same hash, fails too
	}

	_, _, err = Consolidate(context.TODO(), newTestDeps(svc), ConsolidateParams{
		HubApp:           hub,
		Sources:          []ConsolidateSource{{WalletApp: s1, AmountMloki: moved}, {WalletApp: s2, AmountMloki: moved}},
		NewIdentityType:  db.CashIdentityPubkey,
		NewIdentityValue: newPk,
	})
	require.Error(t, err)

	// The merged wallet is RETAINED (the F-1 fix) — good, funds not orphaned.
	require.Equal(t, 3, mergedChildCount(t, svc, hub), "merged wallet must be retained after a failed reversal")

	// Identify the retained merged wallet (the child that is neither s1 nor s2).
	var children []db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&children).Error)
	var mergedID uint
	for i := range children {
		if children[i].ID != s1.ID && children[i].ID != s2.ID {
			mergedID = children[i].ID
		}
	}
	require.NotZero(t, mergedID, "expected a retained merged wallet app")

	s1Balance := queries.GetIsolatedBalance(svc.DB, s1.ID)
	s2Balance := queries.GetIsolatedBalance(svc.DB, s2.ID)
	mergedBalance := queries.GetIsolatedBalance(svc.DB, mergedID)

	// The load-bearing fact: source 1's funds LEFT s1 (transfer 1's outgoing leg
	// settled) and were NEVER returned (the reversal failed) — so s1 is now
	// permanently short by the moved amount until an operator reconciles.
	assert.Equal(t, int64(200_000-moved), s1Balance,
		"s1 is short by the moved amount: it debited the merged wallet and the failed reversal never returned it")
	assert.Equal(t, int64(200_000), s2Balance, "s2 never funded the merged wallet, so its balance is intact")

	// NOTE ON THE MOCK: mergedBalance reads 0 here, not `moved`. That is the
	// single-payee-per-test limitation the author's own rollback test documents
	// — the mock settles the source's OUTGOING leg but not the merged wallet's
	// INCOMING self-payment leg (IsSelfPayment is gated on one mockLN.Pubkey per
	// test). In production the merged wallet IS credited, so the funds are
	// stranded there (F-1's whole reason for retaining the wallet). Either way
	// the residual holds: s1's real backing dropped by `moved`.
	_ = mergedBalance

	// This is the precondition the controller-level fix depends on: s1's real
	// balance is short by `moved`, so the controller must NOT restore s1's
	// source slice to its full original amount here (it now doesn't — see
	// TestConsolidate_StrandedSource_ClaimLeftInPlace).
	t.Logf("s1 real balance=%d after a failed reversal (short by %d) — the controller must leave s1's claim in place, not restore it", s1Balance, moved)
}
