package transactions

// Independent Security Engagement B (2026-08-02) — cash_redeem redeem-fee
// reconciliation, concurrency angle.
//
// The mechanism under review (reconcileCashRedeemFee, called from
// markTransactionSettled whenever an OUTGOING transaction carries a non-nil
// CashRedeemFeeMloki) moves the SIGNED delta between a redemption's quoted
// hub fee and its real Lightning routing fee between a cash_wallet and its
// parent cash_hub, via two brand-new, deterministically-hashed synthetic
// Transaction rows (see deriveCashRedeemFeePaymentHash).
//
// This file asks: what happens when TWO DIFFERENT slices of the SAME shared
// cash_wallet are redeemed concurrently, each settling (and therefore each
// calling reconcileCashRedeemFee) around the same moment? The two
// redemptions carry distinct PaymentHash values (distinct invoices, as two
// real recipients' redemptions always would), so markTransactionSettled's
// own payment-hash-scoped idempotency guard (the postgres
// `Clauses(clause.Locking{Strength: "UPDATE"})` / sqlite-serializes-writes
// dedup check at the top of that function) does NOT serialize them against
// each other the way it would for two racing settlements of the SAME
// payment. Do the two concurrent reconciliations ever interfere with each
// other, deadlock, or leave the wallet's/hub's ledger in a wrong state?
//
// FINDING: no defect found. Verified below by actually running both
// redemptions concurrently (real goroutines, real overlapping DB
// transactions via an artificial payment delay on one leg) rather than
// only sequentially. The reason this holds: reconcileCashRedeemFee never
// reads-then-writes a shared mutable counter — every balance in this system
// (queries.GetIsolatedBalance) is a SUM over the transactions table computed
// fresh at read time, and reconcileCashRedeemFee only ever INSERTs new,
// uniquely-hashed rows (never updates an existing aggregate). Two concurrent
// inserts referencing the same two apps (wallet, hub) commute freely under
// SUM regardless of interleaving or commit order, so there's nothing here
// for a race to corrupt. This test pins that property down concretely: one
// leg has delta > 0 (wallet pays hub) and the other has delta < 0 (hub
// reimburses wallet), running concurrently, specifically to exercise BOTH
// debit/credit orderings (walletApp->hubApp and hubApp->walletApp) at once
// — the shape most likely to reveal a lock-ordering deadlock, if one
// existed.
//
// See also apps/delete_app_orphan_race_test.go's
// TestDeleteAppAndCreateChild_ConcurrentRace_NeverOrphans for the sibling
// concurrency guard on cash_hub child creation/deletion — this file focuses
// solely on the NEW redeem-fee reconciliation path.

import (
	"sync"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCashAuditSecB_ConcurrentRedemptions_SameWallet_OppositeDeltaSigns
// redeems two distinct slices of the SAME cash_wallet concurrently — one
// whose real routing fee undercuts its quote (delta > 0, wallet -> hub) and
// one whose real routing fee overshoots its quote (delta < 0, hub ->
// wallet) — and asserts the final ledger is exactly what sequential
// execution would produce, with no error, panic, or deadlock from either
// goroutine.
func TestCashAuditSecB_ConcurrentRedemptions_SameWallet_OppositeDeltaSigns(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := &db.App{Name: "cash-hub", AppPubkey: auditRandHex32(), Kind: db.AppKindCashHub}
	require.NoError(t, svc.DB.Create(hub).Error)

	// Slice A: claimed 124000, quoted fee 1000 -> net 123000 (matches the
	// fixed-amount tests.MockInvoice exactly). Real fee 300 -> delta_A =
	// +700 (wallet pays the hub the surplus).
	const claimedA = int64(124000)
	const quotedFeeA = int64(1000)
	const realFeeA = int64(300)
	const netA = claimedA - quotedFeeA

	// Slice B: claimed 50000, quoted fee 200 -> net 49800 (paid via the
	// amountless tests.MockZeroAmountInvoice with an explicit override).
	// Real fee 900 -> delta_B = -700 (the hub reimburses the wallet the
	// shortfall) — the OPPOSITE sign from slice A, so the two concurrent
	// reconciliations move value in opposite directions between the same
	// two apps at (near) the same instant.
	const claimedB = int64(50000)
	const quotedFeeB = int64(200)
	const realFeeB = int64(900)
	const netB = claimedB - quotedFeeB

	// Padding beyond the two slices' exact sum: while a redemption's own
	// FeeReserveMloki requirement is waived for cash_claim_slice payments
	// (skipFeeReserve — see validateCanPay), the STORED FeeReserveMloki on a
	// still-PENDING sibling redemption's own row is not, and is counted
	// against the shared pool for as long as that sibling stays PENDING
	// (queries.GetIsolatedBalance sums fee_reserve_mloki for every PENDING
	// outgoing row). Without this headroom, forcing slice A to stay PENDING
	// for a while (via mockLnA.PaymentDelay below, to guarantee real
	// concurrent overlap with slice B) would make slice B's own,
	// legitimate, fully-backed redemption transiently fail with
	// insufficient balance purely because of A's temporary reserve hold —
	// a real but SEPARATE, pre-existing (not redeem-fee-specific) quirk of
	// the exactly-funded shared-pool model noted in this file's own doc
	// comment, not what this test is targeting. Padding isolates the
	// property this test DOES target — reconcileCashRedeemFee's own
	// concurrency-safety — from that unrelated transient effect.
	const padding = int64(20000)
	funded := claimedA + claimedB + padding

	wallet := &db.App{
		Name:        "cash-wallet",
		AppPubkey:   auditRandHex32(),
		Kind:        db.AppKindCashWallet,
		ParentAppID: &hub.ID,
		ParentKind:  db.ParentKindCash,
	}
	require.NoError(t, svc.DB.Create(wallet).Error)
	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId: wallet.ID,
		Scope: constants.CASH_REDEEM_SCOPE,
		// Unbounded MaxAmountLoki: this test isolates the concurrency
		// property from unrelated quota math.
	}).Error)
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:       &wallet.ID,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_SETTLED,
		AmountMloki: uint64(funded), //nolint:gosec // test-controlled positive value
		PaymentHash: auditRandHex32(),
	}).Error)

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)

	// Two INDEPENDENT MockLn instances — tests.MockLn's internal response
	// queue is a plain, un-mutexed slice, so sharing one instance across two
	// concurrently-calling goroutines would itself be a data race in the
	// TEST HARNESS, unrelated to the code under test. Each leg gets its own
	// mock so the only shared, concurrently-accessed resource left is the
	// real target of this test: svc.DB.
	mockLnA := &tests.MockLn{}
	mockLnA.PayInvoiceResponses = append(mockLnA.PayInvoiceResponses, &lnclient.PayInvoiceResponse{
		Preimage: "concurrent-a-preimage",
		Fee:      uint64(realFeeA), //nolint:gosec // test-controlled positive value
	})
	mockLnA.PayInvoiceErrors = append(mockLnA.PayInvoiceErrors, nil)
	// Slow A down so B's settlement genuinely lands while A's own SendPaymentSync
	// call (and specifically its later markTransactionSettled call) is still
	// in flight, forcing real interleaving instead of accidental sequencing.
	delay := 150 * time.Millisecond
	mockLnA.PaymentDelay = &delay

	mockLnB := &tests.MockLn{}
	mockLnB.PayInvoiceResponses = append(mockLnB.PayInvoiceResponses, &lnclient.PayInvoiceResponse{
		Preimage: "concurrent-b-preimage",
		Fee:      uint64(realFeeB), //nolint:gosec // test-controlled positive value
	})
	mockLnB.PayInvoiceErrors = append(mockLnB.PayInvoiceErrors, nil)

	var wg sync.WaitGroup
	var txnA, txnB *Transaction
	var errA, errB error

	wg.Add(2)
	go func() {
		defer wg.Done()
		amt := uint64(netA) //nolint:gosec // test-controlled positive value
		txnA, errA = txnSvc.SendPaymentSync(
			tests.MockInvoice, &amt,
			map[string]interface{}{"cash_claim_slice": true, "cash_redeem_fee_mloki": uint64(quotedFeeA)}, //nolint:gosec // test-controlled positive value
			mockLnA, &wallet.ID, nil,
		)
	}()
	go func() {
		defer wg.Done()
		// Give A a head start into its "create pending row" DB transaction
		// before B starts, so the two really do overlap mid-flight rather
		// than B racing to start first.
		time.Sleep(20 * time.Millisecond)
		amt := uint64(netB) //nolint:gosec // test-controlled positive value
		txnB, errB = txnSvc.SendPaymentSync(
			tests.MockZeroAmountInvoice, &amt,
			map[string]interface{}{"cash_claim_slice": true, "cash_redeem_fee_mloki": uint64(quotedFeeB)}, //nolint:gosec // test-controlled positive value
			mockLnB, &wallet.ID, nil,
		)
	}()
	wg.Wait()

	require.NoError(t, errA, "slice A's concurrent redemption must not fail or deadlock")
	require.NoError(t, errB, "slice B's concurrent redemption must not fail or deadlock")
	require.NotNil(t, txnA)
	require.NotNil(t, txnB)
	assert.Equal(t, uint64(netA), txnA.AmountMloki) //nolint:gosec // test-controlled positive value
	assert.Equal(t, uint64(netB), txnB.AmountMloki) //nolint:gosec // test-controlled positive value

	// The wallet's total debit across both redemptions must be EXACTLY
	// claimedA + claimedB (the fairness invariant NIP-CASH §The Redeem Fee
	// states), regardless of how the two concurrent reconciliations
	// interleaved.
	walletBalance := queries.GetIsolatedBalance(svc.DB, wallet.ID)
	assert.Equal(t, padding, walletBalance,
		"both slices' redemptions, reconciled concurrently, must drain the wallet by exactly their combined claimed amounts, leaving only the untouched padding behind")

	// The hub's net change is exactly delta_A + delta_B = 700 + (-700) = 0:
	// it received A's 700 mloki surplus and paid out B's 700 mloki shortfall.
	// A wrong interleaving (e.g. one reconciliation silently lost, or a
	// lock-ordering issue causing one leg's insert to apply against the
	// wrong app) would show up here as a nonzero hub balance.
	hubBalance := queries.GetIsolatedBalance(svc.DB, hub.ID)
	assert.Equal(t, int64(0), hubBalance,
		"the hub's opposite-signed deltas from the two concurrent redemptions must net to exactly zero")

	// Exactly 2 debit + 2 credit reconciliation rows should exist (one pair
	// per redemption) — not 0 (silently dropped), not more (double-applied).
	var reconciliationRowCount int64
	require.NoError(t, svc.DB.Model(&db.Transaction{}).
		Where("description = ?", "Cash redeem fee reconciliation").
		Count(&reconciliationRowCount).Error)
	assert.Equal(t, int64(4), reconciliationRowCount,
		"exactly one debit+credit pair per concurrently-settled redemption, no duplicates or drops")
}
