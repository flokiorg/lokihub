package transactions

// Shared JIT wallets (see jitwallet/create.go, nip47/controllers/claim_funds_controller.go)
// let several recipients each claim their own slice of ONE wallet's isolated
// balance. enforceJITFullDrain's whole-wallet-balance check is wrong for that
// case — it would reject a recipient's payout whenever OTHER recipients'
// unclaimed slices are still sitting in the same balance. claim_funds
// exempts itself via metadata["jit_claim_slice"] = true (mirroring the
// existing internal_transfer exemption), since it already enforces the
// correct, stronger, per-slice exact-amount rule itself before calling
// SendPaymentSync.
//
// TestSendPaymentSync_JITWallet_ClaimSlice_BypassesWholeWalletFullDrainCheck
// proves the bypass actually works (a payment that would leave a lot of
// balance behind — as a shared-wallet slice claim always does — succeeds
// when the flag is set, and is rejected when it isn't, all else equal).
// TestSendPaymentSync_JITWallet_ClaimSlice_OtherSlicesUnaffected goes further:
// it simulates all three recipients of a shared wallet claiming in sequence
// and asserts each payment only ever debits its own slice, never touching
// the others' — the property that actually makes the shared-pool model safe.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
)

// newSharedJITWallet creates a jit_wallet child (bypassing jitwallet.Create,
// per this package's own established pattern in jit_internal_transfer_test.go)
// granted JIT_CLAIM_FUNDS_SCOPE (the scope a real shared JIT wallet carries),
// with a budget cap generous enough that CalculateFeeReserveMloki's headroom
// requirement on each individual payment doesn't spuriously trip the quota
// check.
func newSharedJITWallet(t *testing.T, svc *tests.TestService, budgetCapLoki int) *db.App {
	t.Helper()
	hub := &db.App{Name: "hub", Kind: db.AppKindJITHub}
	require.NoError(t, svc.DB.Create(hub).Error)

	wallet := &db.App{
		Name:        "jit-wallet",
		Kind:        db.AppKindJITWallet,
		ParentAppID: &hub.ID,
		ParentKind:  db.ParentKindJIT,
	}
	require.NoError(t, svc.DB.Create(wallet).Error)

	perm := &db.AppPermission{
		AppId:         wallet.ID,
		App:           *wallet,
		Scope:         constants.JIT_CLAIM_FUNDS_SCOPE,
		MaxAmountLoki: budgetCapLoki,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
	}
	require.NoError(t, svc.DB.Create(perm).Error)
	return wallet
}

// TestSendPaymentSync_JITWallet_ClaimSlice_ExactBalance_NoFeeReserveHeadroomNeeded
// covers a critical production-correctness fix: validateCanPay's fee-reserve
// headroom (amount + max(1% of amount, 10_000 mloki) <= balance) is
// mathematically unsatisfiable for a wallet funded with EXACTLY the amount
// being claimed — which is exactly how a shared JIT wallet's last (or only)
// recipient's slice works (jitwallet.Commit funds the wallet with the exact
// sum of declared recipient amounts, and claim_funds requires the invoice
// amount to exactly equal the claimed slice). Without skipFeeReserve, no
// real (non-self) claim_funds payout could ever succeed, regardless of
// amount.
func TestSendPaymentSync_JITWallet_ClaimSlice_ExactBalance_NoFeeReserveHeadroomNeeded(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Budget cap AND real balance both set to EXACTLY the amount MockInvoice
	// decodes to (123,000 mloki) — no headroom at all, matching a real
	// last-recipient claim on a shared wallet.
	wallet := newSharedJITWallet(t, svc, 123)
	svc.DB.Create(&db.Transaction{
		AppId: &wallet.ID, State: constants.TRANSACTION_STATE_SETTLED,
		Type: constants.TRANSACTION_TYPE_INCOMING, AmountMloki: 123_000,
	})

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(dbRequestEvent).Error)
	_, err = transactionsService.SendPaymentSync(
		tests.MockInvoice, nil, map[string]interface{}{"jit_claim_slice": true},
		svc.LNClient, &wallet.ID, &dbRequestEvent.ID,
	)
	assert.NoError(t, err, "a wallet funded with exactly the claimed amount must still be payable via claim_funds — no fee-reserve headroom should be required")

	balanceAfter := queries.GetIsolatedBalance(svc.DB, wallet.ID)
	assert.LessOrEqual(t, balanceAfter, int64(0), "the full exact balance must have been drained")
}

func TestSendPaymentSync_JITWallet_ClaimSlice_BypassesWholeWalletFullDrainCheck(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// A 3-recipient shared wallet: 500,000 mloki total, only one recipient's
	// 100,000-mloki slice is being claimed — a real leftover of 400,000
	// mloki, far more than the fee-reserve floor, which enforceJITFullDrain
	// would normally reject.
	wallet := newSharedJITWallet(t, svc, 500)
	svc.DB.Create(&db.Transaction{
		AppId: &wallet.ID, State: constants.TRANSACTION_STATE_SETTLED,
		Type: constants.TRANSACTION_TYPE_INCOMING, AmountMloki: 500_000,
	})

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)

	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(dbRequestEvent).Error)
	_, err = transactionsService.SendPaymentSync(
		tests.MockInvoice, nil, nil, svc.LNClient, &wallet.ID, &dbRequestEvent.ID,
	)
	assert.Error(t, err, "without the jit_claim_slice exemption, a partial-of-shared-balance payment must still be rejected as a partial spend")

	dbRequestEvent2 := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(dbRequestEvent2).Error)
	_, err = transactionsService.SendPaymentSync(
		tests.MockInvoice, nil, map[string]interface{}{"jit_claim_slice": true},
		svc.LNClient, &wallet.ID, &dbRequestEvent2.ID,
	)
	assert.NoError(t, err, "jit_claim_slice=true must bypass the whole-wallet full-drain check")
}

func TestSendPaymentSync_JITWallet_ClaimSlice_OtherSlicesUnaffected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Three recipients sharing one wallet, 200,000 mloki each (600,000 total).
	const sliceMloki = 200_000
	wallet := newSharedJITWallet(t, svc, 600)
	svc.DB.Create(&db.Transaction{
		AppId: &wallet.ID, State: constants.TRANSACTION_STATE_SETTLED,
		Type: constants.TRANSACTION_TYPE_INCOMING, AmountMloki: 3 * sliceMloki,
	})

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	balanceBefore := queries.GetIsolatedBalance(svc.DB, wallet.ID)
	require.EqualValues(t, 3*sliceMloki, balanceBefore)

	// Recipient 1 claims their slice. tests.MockInvoice always decodes to a
	// fixed 123,000 mloki amount regardless of what's "intended" — that's
	// fine here, since this test asserts the balance moves by exactly the
	// payment's real amount and nothing more, not that it matches sliceMloki.
	dbRequestEvent := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(dbRequestEvent).Error)
	_, err = transactionsService.SendPaymentSync(
		tests.MockInvoice, nil, map[string]interface{}{"jit_claim_slice": true},
		svc.LNClient, &wallet.ID, &dbRequestEvent.ID,
	)
	require.NoError(t, err)

	balanceAfterFirst := queries.GetIsolatedBalance(svc.DB, wallet.ID)
	assert.Greater(t, balanceAfterFirst, int64(0),
		"recipients 2 and 3's slices must still be present — this must be a partial deduction, not a full wallet drain")
	assert.Less(t, balanceAfterFirst, balanceBefore, "recipient 1's payment must have deducted something")

	// Recipient 2 claims next, using a distinct invoice/payment_hash — the
	// mock's global "already paid" check is keyed on payment_hash, not app,
	// so two independent real payments from the same test need distinct
	// canned invoices (see tests.MockZeroAmountInvoice for the second one).
	dbRequestEvent2 := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(dbRequestEvent2).Error)
	_, err = transactionsService.SendPaymentSync(
		tests.MockZeroAmountInvoice, ptrUint64Transactions(50_000), map[string]interface{}{"jit_claim_slice": true},
		svc.LNClient, &wallet.ID, &dbRequestEvent2.ID,
	)
	require.NoError(t, err, "recipient 2's independent claim must succeed without interference from recipient 1's already-settled payment")

	balanceAfterSecond := queries.GetIsolatedBalance(svc.DB, wallet.ID)
	assert.Greater(t, balanceAfterSecond, int64(0), "recipient 3's slice must still be present")
	assert.Less(t, balanceAfterSecond, balanceAfterFirst, "recipient 2's payment must have deducted something on top of recipient 1's")
}

func ptrUint64Transactions(v uint64) *uint64 { return &v }
