package transactions

// Independent Security Engagement B (2026-08-02) — cash_redeem redeem-fee
// reconciliation, "zero-parent-hub" fallback and defensive-guard angle.
//
// reconcileCashRedeemFee (transactions_service.go ~line 1940) had two
// defensive-guard gaps identified here, both since addressed:
//
//  1. `if walletApp.ParentAppID == nil { ...log...; return nil }` — silently
//     skips reconciliation entirely. Confirmed unreachable via any real
//     application code path (cashwallet.Commit/Split always set
//     ParentAppID; apps.deleteHubAppTx refuses to delete a cash_hub with
//     live cash_wallet children — see apps/delete_app_orphan_race_test.go).
//     Reachable only via direct DB manipulation bypassing the app layer.
//     Deliberately KEPT as a log-and-skip, not converted to a hard error:
//     by the time this function runs, the real payment has already
//     unconditionally happened (a real Lightning send, or an already-
//     settled self-payment) — see reconcileCashRedeemFee's own updated doc
//     comment for why returning an error here would roll back the
//     enclosing settlement transaction's "mark SETTLED" update for a
//     payment that genuinely already went out, reopening an already-paid
//     cash_redeem slice for a second claim (a real double-payout risk,
//     strictly worse than skipping one reconciliation adjustment).
//
//  2. `*dbTransaction.AppId` — was dereferenced unconditionally, with no
//     nil check (unlike the ParentAppID nil check right after it) — a
//     transaction with CashRedeemFeeMloki set but AppId nil panicked
//     instead of erroring cleanly. FIXED: a nil check now precedes the
//     dereference, logging and skipping reconciliation exactly like every
//     other guard in this function, rather than panicking.

import (
	"testing"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCashAuditSecB_ReconcileCashRedeemFee_NilParentHub_SilentlySwallowsReconciliation
// hand-crafts a cash_wallet app with ParentAppID == nil — a state normal
// application code can never produce (see this file's own doc comment and
// apps/delete_app_orphan_race_test.go) — and confirms exactly what the code
// under review does in that state: the payout settles normally (the
// recipient is paid, fee-reduced, in full), but the wallet<->hub
// reconciliation that should have moved the delta is silently skipped, with
// no error surfaced to any caller and no compensating transaction anywhere.
// If this state were ever reached in production (e.g. by a bug elsewhere,
// or manual DB surgery), a real, nonzero delta would be permanently and
// silently lost rather than causing a loud, debuggable failure.
func TestCashAuditSecB_ReconcileCashRedeemFee_NilParentHub_SilentlySwallowsReconciliation(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Deliberately no parent hub app at all — ParentAppID left nil, unlike
	// every real cash_wallet (cashwallet.Commit/Split always set it).
	wallet := &db.App{
		Name:       "orphan-cash-wallet",
		AppPubkey:  auditRandHex32(),
		Kind:       db.AppKindCashWallet,
		ParentKind: db.ParentKindCash,
		// ParentAppID: nil (zero value)
	}
	require.NoError(t, svc.DB.Create(wallet).Error)
	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId: wallet.ID,
		Scope: constants.CASH_REDEEM_SCOPE,
	}).Error)

	// net (claimed - quotedFee) is chosen to equal tests.MockInvoice's own
	// fixed embedded amount (123000) exactly, since a fixed-amount bolt11
	// invoice ignores SendPaymentSync's amount override — mirroring
	// TestCashAuditFin_MultiRecipientRedeemFee_FixedByRedeemFeePpm's own
	// setup in cash_audit_fin_redeem_fee_test.go.
	const claimed = int64(124000)
	const quotedFee = int64(1000)
	const realFee = int64(300) // delta = +700, which SHOULD move wallet -> hub but has no hub to move to
	const net = claimed - quotedFee

	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:       &wallet.ID,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_SETTLED,
		AmountMloki: uint64(claimed), //nolint:gosec // test-controlled positive value
		PaymentHash: auditRandHex32(),
	}).Error)

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)
	mockLn := &tests.MockLn{}
	mockLn.PayInvoiceResponses = append(mockLn.PayInvoiceResponses, &lnclient.PayInvoiceResponse{
		Preimage: "orphan-hub-preimage",
		Fee:      uint64(realFee), //nolint:gosec // test-controlled positive value
	})
	mockLn.PayInvoiceErrors = append(mockLn.PayInvoiceErrors, nil)

	amt := uint64(net) //nolint:gosec // test-controlled positive value
	txn, payErr := txnSvc.SendPaymentSync(
		tests.MockInvoice, &amt,
		map[string]interface{}{"cash_claim_slice": true, "cash_redeem_fee_mloki": uint64(quotedFee)}, //nolint:gosec // test-controlled positive value
		mockLn, &wallet.ID, nil,
	)

	// The payout itself succeeds — the recipient is paid in full,
	// fee-reduced — exactly as if reconciliation had worked.
	require.NoError(t, payErr, "the payout completes even though its parent hub is missing — no error is surfaced")
	require.NotNil(t, txn)
	assert.Equal(t, uint64(net), txn.AmountMloki) //nolint:gosec // test-controlled positive value

	// But the 700 mloki delta that SHOULD have moved from the wallet to its
	// (nonexistent) parent hub never went anywhere: no reconciliation rows
	// exist at all.
	var reconciliationRowCount int64
	require.NoError(t, svc.DB.Model(&db.Transaction{}).
		Where("description = ?", "Cash redeem fee reconciliation").
		Count(&reconciliationRowCount).Error)
	assert.Equal(t, int64(0), reconciliationRowCount,
		"FINDING: with a nil ParentAppID, reconcileCashRedeemFee logs an error and silently returns nil — "+
			"no reconciliation row is ever created, and no error propagates to the caller")

	// The wallet's own isolated balance reflects only the real payout
	// (net + realFee) — the 700 mloki surplus it should have paid to the
	// hub stays sitting in the wallet's own balance forever, uncollected
	// and unaccounted for anywhere else, rather than either landing at the
	// hub (the correct outcome) or at least being visibly flagged.
	walletBalance := queries.GetIsolatedBalance(svc.DB, wallet.ID)
	assert.Equal(t, claimed-(net+realFee), walletBalance,
		"the quoted-fee surplus (700 mloki) is stranded in the wallet's own balance, "+
			"neither collected by the (missing) hub nor flagged as an accounting discrepancy")
}

// TestCashAuditSecB_ReconcileCashRedeemFee_NilAppId_SkipsCleanly is finding #2
// from this file's package doc comment: reconcileCashRedeemFee used to
// dereference `*dbTransaction.AppId` with no nil check, one line before its
// (present) nil check on walletApp.ParentAppID — a transaction with
// CashRedeemFeeMloki set but AppId nil panicked instead of erroring cleanly.
// FIXED: a nil check now precedes the dereference. This test confirms the
// fix: the same nil-AppId pairing no longer panics — the payment settles
// normally and reconciliation is safely skipped with a log line, exactly
// like every other guard in this function.
//
// Nothing reachable via any external NIP-47/HTTP entry point can currently
// produce a transaction with CashRedeemFeeMloki set and AppId nil —
// cash_redeem_controller.go, the only real caller of the
// "cash_redeem_fee_mloki" metadata key, always passes &app.ID — so this is
// exercised here by calling the exported transactions.SendPaymentSync
// directly with that exact (still synthetic) combination, the same
// technique transactions/cash_audit_fin_redeem_fee_test.go and this
// package's other audit tests already use to reach the mechanism below the
// NIP-47 layer.
func TestCashAuditSecB_ReconcileCashRedeemFee_NilAppId_SkipsCleanly(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)
	mockLn := &tests.MockLn{}
	mockLn.PayInvoiceResponses = append(mockLn.PayInvoiceResponses, &lnclient.PayInvoiceResponse{
		Preimage: "nil-appid-preimage",
		Fee:      uint64(0),
	})
	mockLn.PayInvoiceErrors = append(mockLn.PayInvoiceErrors, nil)

	amt := uint64(1000)
	// appId is nil here — the pairing no real caller currently produces, but
	// nothing in SendPaymentSync's own validation rejects it. Must not panic.
	txn, payErr := txnSvc.SendPaymentSync(
		tests.MockInvoice, &amt,
		map[string]interface{}{"cash_claim_slice": true, "cash_redeem_fee_mloki": uint64(500)},
		mockLn, nil, nil,
	)
	require.NoError(t, payErr, "a nil AppId must no longer panic — reconciliation is skipped cleanly instead")
	require.NotNil(t, txn)
	assert.Equal(t, uint64(123_000), txn.AmountMloki) // tests.MockInvoice's own fixed amount — amt override is ignored for a non-zero-amount invoice

	var reconciliationRowCount int64
	require.NoError(t, svc.DB.Model(&db.Transaction{}).
		Where("description = ?", "Cash redeem fee reconciliation").
		Count(&reconciliationRowCount).Error)
	assert.Zero(t, reconciliationRowCount, "no reconciliation row can be created without a real AppId to attribute it to")
}
