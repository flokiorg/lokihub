package cashwallet

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
	"github.com/nbd-wtf/go-nostr"
)

// TestAuditAQA_LNMock_CreditsTheMockAmountNotTheComputedAmount measures what a
// unit-test internal transfer actually moves, rather than assuming it moves the
// amount cashwallet computed.
//
// Diagnostic only: it always passes, and prints the three numbers.
func TestAuditAQA_LNMock_CreditsTheMockAmountNotTheComputedAmount(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "qafund")

	const requested = uint64(5000) // deliberately NOT 1000

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(requested),
		ExpirySecs: 1800,
	})
	require.NoError(t, err)

	var incoming db.Transaction
	require.NoError(t, svc.DB.Where("app_id = ? AND type = ?",
		result.WalletApp.ID, constants.TRANSACTION_TYPE_INCOMING).First(&incoming).Error)

	var outgoing db.Transaction
	require.NoError(t, svc.DB.Where("app_id = ? AND type = ?",
		hub.ID, constants.TRANSACTION_TYPE_OUTGOING).First(&outgoing).Error)

	t.Logf("requested (what cashwallet computed) = %d mloki", requested)
	t.Logf("claim row AmountMloki               = %d mloki", result.Recipients[0].AmountMloki)
	t.Logf("bill's credited INCOMING leg        = %d mloki  <- lnClientTransaction.Amount", incoming.AmountMloki)
	t.Logf("hub's debited OUTGOING leg          = %d mloki  <- decodepay(MockInvoice)", outgoing.AmountMloki)
	t.Logf("settled? incoming=%s outgoing=%s", incoming.State, outgoing.State)
}

// TestAuditAQA_FundInternalOverride_CanAssertTheComputedAmount is the closure:
// the seam already exists, it is just never asked about the amount.
//
// Both current users of FundInternalOverride discard the amount argument
// (`func(_ context.Context, fromAppID, toAppID uint, _ uint64, _ string)`), so
// nothing anywhere asserts what a saga funds. Recording it costs four lines and
// makes every amount in Create/Split/Consolidate assertable with no bolt11
// encoder and no LN mock change.
func TestAuditAQA_FundInternalOverride_CanAssertTheComputedAmount(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "qafund2")

	type move struct {
		from, to uint
		amount   uint64
	}
	var moves []move

	deps := newTestDeps(svc)
	deps.FundInternalOverride = func(_ context.Context, fromAppID, toAppID uint, amountMloki uint64, _ string) error {
		moves = append(moves, move{fromAppID, toAppID, amountMloki})
		return nil
	}

	const requested = uint64(5000)
	result, err := Create(context.TODO(), deps, Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(requested),
		ExpirySecs: 1800,
	})
	require.NoError(t, err)

	require.Len(t, moves, 1, "Create funds the new bill with exactly one internal transfer")
	require.Equal(t, hub.ID, moves[0].from, "the funding must come from the hub")
	require.Equal(t, result.WalletApp.ID, moves[0].to, "and land on the new bill")
	require.Equal(t, requested, moves[0].amount,
		"Create must fund the bill with the summed claim amount, not a scaled or truncated one")
}

// TestAuditAQA_Consolidate_DebitIgnoresTheRequestedAmount is the demonstration
// that `const moved = uint64(123_000)` in consolidate_rollback_residual_b_test.go
// is a fixture whose value comes from the MOCK, not from production.
//
// 123_000 mloki is exactly decodepay(tests.MockInvoice).AmountMloki. The
// ConsolidateSource.AmountMloki argument plays no causal part in what is debited:
// the debit is whatever the bolt11 string encodes. This test asks for DOUBLE and
// shows the debit does not move.
//
// Diagnostic: always passes, prints the numbers.
func TestAuditAQA_Consolidate_DebitIgnoresTheRequestedAmount(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	s1 := newConsolidateSourceApp(t, svc, hub, "s1", 200_000, "qa-s1-fund")
	s2 := newConsolidateSourceApp(t, svc, hub, "s2", 200_000, "qa-s2-fund")
	newPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	// The residual_b fixture's value, and DOUBLE it as the requested amount.
	const mockBolt11Amount = uint64(123_000)
	const requested = mockBolt11Amount * 2

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "q1", Amount: int64(mockBolt11Amount)},
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "q2", Amount: int64(mockBolt11Amount)},
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "q3", Amount: int64(mockBolt11Amount)},
	}

	_, _, _ = Consolidate(context.TODO(), newTestDeps(svc), ConsolidateParams{
		HubApp: hub,
		Sources: []ConsolidateSource{
			{WalletApp: s1, AmountMloki: requested},
			{WalletApp: s2, AmountMloki: requested},
		},
		NewIdentityType:  db.CashIdentityPubkey,
		NewIdentityValue: newPk,
	})

	s1Balance := queries.GetIsolatedBalance(svc.DB, s1.ID)
	t.Logf("requested per-source debit          = %d mloki", requested)
	t.Logf("decodepay(tests.MockInvoice) amount = %d mloki", mockBolt11Amount)
	t.Logf("s1 balance after                    = %d (started at 200000, so debited %d)",
		s1Balance, 200_000-s1Balance)
	t.Logf("=> the requested amount had no effect on what was debited")
}
