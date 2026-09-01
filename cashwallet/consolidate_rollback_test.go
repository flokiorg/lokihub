package cashwallet

import (
	"context"
	"fmt"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// TestConsolidate_MidLoopFailure_ReversalItselfFails_MergedWalletRetained below
// reaches Consolidate's compensating-rollback defer with a real failure rather
// than a synthetic fault-injection seam: the mock LN client's SendPaymentSync
// decodes the actual bolt11 text through the same decodepay path production
// uses, and the transactions service's "already paid" guard is keyed on that
// decoded payment_hash — so replaying tests.MockInvoice a second (and third)
// time is a genuine failure the production code path produces on its own, not
// one a test forces open a side door for.
//
// The mirror case — a mid-loop failure whose reversal SUCCEEDS, so the merged
// wallet is deleted — needed a different technique: two invoices with distinct
// payment hashes that ALSO settle the recipient's incoming leg, which in the
// mock only happens via self-payment interception, gated on the invoice's
// embedded payee matching mockLN.Pubkey (see IsSelfPayment). Every canned
// invoice fixture has a distinct payee baked into its real signature, so no
// two of them can both settle within one Consolidate call sharing a single
// mockLN.Pubkey — the same wall the QA audit lead flagged as making these
// paths hard to unit-test. Deps.FundInternalOverride (see
// TestConsolidate_MidLoopFailure_RollbackSucceeds_DeletesMergedWallet below)
// closes that gap without needing more invoice fixtures.

func newConsolidateSourceApp(t *testing.T, svc *tests.TestService, hub *db.App, name string, balanceMloki uint64, fundTxHash string) *db.App {
	t.Helper()
	app, _, err := svc.AppsService.CreateApp(
		name, "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.CASH_CONSOLIDATE_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, app.ID, balanceMloki, fundTxHash)
	return app
}

func mergedChildCount(t *testing.T, svc *tests.TestService, hub *db.App) int {
	t.Helper()
	var apps []db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&apps).Error)
	return len(apps)
}

// TestConsolidate_MidLoopFailure_ReversalItselfFails_MergedWalletRetained is the
// regression for the audit fix: source 2's funding fails the same way as
// above, but this time the compensating reversal ALSO reuses the
// already-settled MockInvoice hash, so it fails too. Before the fix, the
// merged wallet was deleted unconditionally after the reversal loop — which
// would silently strand source 1's funds under no wallet at all. The fix
// requires the merged wallet to survive so the stranded funds stay visible
// for a manual sweep.
func TestConsolidate_MidLoopFailure_ReversalItselfFails_MergedWalletRetained(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	s1 := newConsolidateSourceApp(t, svc, hub, "s1", 200_000, "s1-fund")
	s2 := newConsolidateSourceApp(t, svc, hub, "s2", 200_000, "s2-fund")
	newPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: 123_000}, // 1: fund merged from s1 — succeeds, settles MockInvoice's hash
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p2", Amount: 123_000}, // 2: fund merged from s2 — same hash: fails, starts rollback
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p3", Amount: 123_000}, // 3: reversal reuses the same settled hash too: fails
	}

	_, strandedSourceAppIDs, err := Consolidate(context.TODO(), newTestDeps(svc), ConsolidateParams{
		HubApp:           hub,
		Sources:          []ConsolidateSource{{WalletApp: s1, AmountMloki: 123_000}, {WalletApp: s2, AmountMloki: 123_000}},
		NewIdentityType:  db.CashIdentityPubkey,
		NewIdentityValue: newPk,
	})
	require.Error(t, err)

	assert.Equal(t, 3, mergedChildCount(t, svc, hub),
		"the merged wallet must be RETAINED (not deleted) since its funds could not be reversed — deleting it here would silently strand them under no wallet at all")
	assert.Equal(t, []uint{s1.ID}, strandedSourceAppIDs,
		"s1's reversal failed, so it must be reported as stranded — the caller must not restore its claim to the full original amount when its real balance is short by exactly what never came back")

	// A durable reconciliation record must exist too, so an operator sweep is
	// driven by a query rather than only the log line above.
	records, err := svc.AppsService.ListCashStrandedFunds(true)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "consolidate", records[0].Operation)
	assert.Equal(t, s1.ID, records[0].SourceWalletAppID)
	assert.EqualValues(t, 123_000, records[0].AmountMloki)
	assert.Nil(t, records[0].ResolvedAt)
	// RetainedWalletAppID must be the merged wallet — neither s1 nor s2.
	assert.NotEqual(t, s1.ID, records[0].RetainedWalletAppID)
	assert.NotEqual(t, s2.ID, records[0].RetainedWalletAppID)
}

// TestConsolidate_MidLoopFailure_RollbackSucceeds_DeletesMergedWallet is the
// mirror of the test above: source 2's forward funding fails, but this time
// source 1's reversal SUCCEEDS, so the merged wallet must end up deleted and
// no source reported as stranded. Reaches this via Deps.FundInternalOverride
// rather than real invoices — the mock's self-payment-interception limit
// documented above previously made this specific branch (a reversal that
// succeeds, in a saga that also had an earlier failure) unreachable in a unit
// test.
func TestConsolidate_MidLoopFailure_RollbackSucceeds_DeletesMergedWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	s1 := newConsolidateSourceApp(t, svc, hub, "s1", 200_000, "s1-fund")
	s2 := newConsolidateSourceApp(t, svc, hub, "s2", 200_000, "s2-fund")
	newPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	type call struct {
		fromAppID, toAppID uint
	}
	var calls []call
	deps := newTestDeps(svc)
	deps.FundInternalOverride = func(_ context.Context, fromAppID, toAppID uint, _ uint64, _ string) error {
		calls = append(calls, call{fromAppID, toAppID})
		switch len(calls) {
		case 1: // fund merged from s1 — succeeds
			return nil
		case 2: // fund merged from s2 — fails, starts rollback
			return fmt.Errorf("simulated forward-funding failure")
		case 3: // reverse s1's transfer — succeeds this time
			return nil
		default:
			t.Fatalf("unexpected 4th internal transfer call: %+v", calls)
			return nil
		}
	}

	_, strandedSourceAppIDs, err := Consolidate(context.TODO(), deps, ConsolidateParams{
		HubApp:           hub,
		Sources:          []ConsolidateSource{{WalletApp: s1, AmountMloki: 123_000}, {WalletApp: s2, AmountMloki: 123_000}},
		NewIdentityType:  db.CashIdentityPubkey,
		NewIdentityValue: newPk,
	})
	require.Error(t, err)
	require.Len(t, calls, 3)

	assert.Equal(t, s1.ID, calls[0].fromAppID, "call 1 funds the merged wallet FROM s1")
	assert.Equal(t, s2.ID, calls[1].fromAppID, "call 2 funds the merged wallet FROM s2")
	assert.Equal(t, s1.ID, calls[2].toAppID, "call 3 reverses TO s1")

	assert.Equal(t, 2, mergedChildCount(t, svc, hub),
		"the merged wallet must be DELETED once its one completed transfer was successfully reversed — only s1 and s2 remain")
	assert.Empty(t, strandedSourceAppIDs, "no source should be reported stranded when its reversal succeeded")

	records, err := svc.AppsService.ListCashStrandedFunds(false)
	require.NoError(t, err)
	assert.Empty(t, records, "no reconciliation record should be written when the reversal succeeded")
}
