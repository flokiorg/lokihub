package transactions

// Independent security audit A (2026-08-02) — Cash Hub redeem-fee mechanism.
//
// FINDING (Medium, FIXED): db.Transaction.CashRedeemFeeMloki's own doc
// comment states it is "set only on a cash_wallet's own outgoing payout row
// for a cash_redeem call" — but reconcileCashRedeemFee (transactions_service.go,
// ~line 1940) did not enforce that: it trusted the presence of a non-nil
// CashRedeemFeeMloki pointer on ANY outgoing db.Transaction row, blindly
// resolved *whatever app* the row belonged to, walked up to *whatever* app
// its ParentAppID happened to point to, and moved a synthetic debit/credit
// pair between the two — without checking that the paying app was really a
// cash_wallet (db.AppKindCashWallet) or that its parent was really a
// cash_hub (db.AppKindCashHub / db.ParentKindCash). Any parent/child app
// pair on this instance (a circle_wallet under a circle_hub, an isolated
// subwallet under its own parent, etc.) was treated identically.
//
// FIXED two ways:
//  1. reconcileCashRedeemFee itself now checks walletApp.Kind ==
//     db.AppKindCashWallet && walletApp.ParentKind == db.ParentKindCash
//     before moving anything, logging and skipping (not erroring — see the
//     function's own doc comment for why an error here would be worse)
//     otherwise.
//  2. pay_invoice_controller.go's pay() and api/transactions.go's
//     SendPayment now both delete("cash_redeem_fee_mloki") from
//     caller-supplied metadata, matching the existing treatment of
//     "internal_transfer"/"cash_claim_slice" — closing the wire-reachability
//     gap regardless of how metadata decoding evolves (see
//     TestCashAuditRedeemFeeSecA_JSONMetadataCannotCarryUint64_AccidentalProtectionOnly
//     below for why this was previously only an accident of JSON decoding,
//     not a deliberate strip).
//
// This test now confirms fix #1 directly: the exact cross-feature scenario
// (a native Go uint64 cash_redeem_fee_mloki targeting a circle_wallet) that
// used to fire a "Cash redeem fee reconciliation" against an unrelated
// circle_wallet/circle_hub pair is now safely skipped instead.
import (
	"encoding/json"
	"testing"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCashAuditRedeemFeeSecA_ReconcileHasAppKindGuard_CrossFeatureLedgerCorruptionPrevented(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// A circle_hub / circle_wallet pair — deliberately NOT a cash_hub/cash_wallet.
	// The redeem-fee mechanism has no business touching this pair at all.
	circleHub := &db.App{Name: "circle-hub", AppPubkey: auditRandHex32(), Kind: db.AppKindCircleHub}
	require.NoError(t, svc.DB.Create(circleHub).Error)

	circleWallet := &db.App{
		Name:        "circle-wallet",
		AppPubkey:   auditRandHex32(),
		Kind:        db.AppKindCircleWallet,
		ParentAppID: &circleHub.ID,
		ParentKind:  db.ParentKindCircle,
	}
	require.NoError(t, svc.DB.Create(circleWallet).Error)
	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId:         circleWallet.ID,
		Scope:         constants.PAY_INVOICE_SCOPE,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		// MaxAmountLoki left at zero (unbounded) — this test isolates the
		// redeem-fee reconciliation gap from unrelated budget-cap math.
	}).Error)
	// Fund the circle_wallet generously — comfortably covers MockInvoice's
	// 123000 mloki amount plus the standard fee-reserve headroom.
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:       &circleWallet.ID,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_SETTLED,
		AmountMloki: 300_000,
		PaymentHash: auditRandHex32(),
	}).Error)

	// A real external payment: quoted "hub fee" of 1000 mloki, real routing
	// fee of only 300 mloki reported back by the LN backend — exactly the
	// shape cash_redeem's own fee quoting produces, except this time the
	// paying app is a circle_wallet, not a cash_wallet.
	const quotedFee = uint64(1000)
	const realFee = uint64(300)
	mockLn := svc.LNClient.(*tests.MockLn)
	mockLn.PayInvoiceResponses = append(mockLn.PayInvoiceResponses, &lnclient.PayInvoiceResponse{
		Preimage: "circle-crossfeature-preimage",
		Fee:      realFee,
	})
	mockLn.PayInvoiceErrors = append(mockLn.PayInvoiceErrors, nil)

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)
	txn, payErr := txnSvc.SendPaymentSync(
		tests.MockInvoice, nil,
		// This is the exact metadata shape cash_redeem_controller.go step 10
		// builds (see cashRedeemFeeMloki in SendPaymentSync,
		// transactions_service.go:458-461) — a native Go uint64, not a
		// caller-supplied JSON value. Nothing in SendPaymentSync or
		// reconcileCashRedeemFee checks WHICH app this metadata is being
		// applied to.
		map[string]interface{}{"cash_redeem_fee_mloki": quotedFee},
		svc.LNClient, &circleWallet.ID, nil,
	)
	require.NoError(t, payErr, "the underlying payment must still succeed - only the fabricated reconciliation must be blocked")
	require.NotNil(t, txn)

	// The redeem-fee flag still lands on the transaction row (SendPaymentSync
	// sets it unconditionally from metadata, independent of app kind) — that
	// alone moves no money. What matters is what reconcileCashRedeemFee does
	// with it next.
	var stored db.Transaction
	require.NoError(t, svc.DB.Where("payment_hash = ? AND app_id = ?", tests.MockPaymentHash, circleWallet.ID).First(&stored).Error)
	require.NotNil(t, stored.CashRedeemFeeMloki)
	assert.Equal(t, quotedFee, *stored.CashRedeemFeeMloki)

	// FIXED: no "Cash redeem fee reconciliation" rows exist anywhere — the
	// app-kind guard inside reconcileCashRedeemFee rejects this circle_wallet/
	// circle_hub pair before moving anything.
	var reconciliationCount int64
	require.NoError(t, svc.DB.Model(&db.Transaction{}).
		Where("description = ?", "Cash redeem fee reconciliation").
		Count(&reconciliationCount).Error)
	assert.Zero(t, reconciliationCount, "reconcileCashRedeemFee's app-kind guard must reject a non-cash_wallet paying app")

	// The circle_wallet's and circle_hub's own balances are consequently
	// untouched by this mechanism — no phantom debit, no phantom credit.
	assert.Equal(t, int64(300_000-123_000-int64(realFee)), queries.GetIsolatedBalance(svc.DB, circleWallet.ID)) //nolint:gosec // test-controlled positive values
	assert.Zero(t, queries.GetIsolatedBalance(svc.DB, circleHub.ID))
}

// TestCashAuditRedeemFeeSecA_JSONMetadataCannotCarryUint64_AccidentalProtectionOnly
// shows WHY the finding above is not (yet) reachable from the wire: a JSON
// number decoded into a map[string]interface{} (exactly how
// nip47/controllers/decode_request.go and Echo's c.Bind() both decode caller
// metadata) always becomes a float64, never a uint64, so
// SendPaymentSync's `metadata["cash_redeem_fee_mloki"].(uint64)` type
// assertion (transactions_service.go:459) silently fails for any
// wire-supplied value and CashRedeemFeeMloki stays nil.
//
// This is worth confirming explicitly rather than assuming it, precisely
// because it is NOT the codebase's own stated defense: pay_invoice's
// "internal_transfer"/"cash_claim_slice" flags are booleans, which DO survive
// a JSON round-trip into interface{} unchanged (JSON true/false -> Go bool),
// which is exactly why those two get an explicit delete() before reaching
// SendPaymentSync (pay_invoice_controller.go:69-72,
// api/transactions.go:88-91) — a deliberate strip, not a lucky type
// mismatch. "cash_redeem_fee_mloki" got no matching delete() at either call
// site (grep confirms it), so today's safety rests entirely on nobody ever
// changing the metadata decode path to preserve integer JSON numbers (e.g.
// switching to json.Number, or a byte-for-byte re-marshal that coerces
// float64 back to uint64) or introducing a new internal caller that builds
// this key from user input as a native Go integer. Either change would
// reopen TestCashAuditRedeemFeeSecA_ReconcileHasAppKindGuard_CrossFeatureLedgerCorruptionPrevented's
// scenario to a real wire-reachable attacker on an ordinary pay_invoice-scoped
// connection, or (worse) on a cash_wallet redeeming through cash_redeem
// itself — since cash_redeem_controller.go's own strip only clears an EMPTY
// map before setting the key itself (step 10), it never had to defend
// against this; the gap is entirely in the OTHER, general-purpose payment
// entry points.
func TestCashAuditRedeemFeeSecA_JSONMetadataCannotCarryUint64_AccidentalProtectionOnly(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId: app.ID,
		Scope: constants.PAY_INVOICE_SCOPE,
	}).Error)

	// Simulate exactly what an attacker-controlled pay_invoice JSON request
	// produces once decoded: {"metadata": {"cash_redeem_fee_mloki": 999999}}.
	raw := []byte(`{"cash_redeem_fee_mloki": 999999}`)
	var wireMetadata map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &wireMetadata))

	v := wireMetadata["cash_redeem_fee_mloki"]
	_, isFloat64 := v.(float64)
	_, isUint64 := v.(uint64)
	assert.True(t, isFloat64, "a JSON-decoded number lands as float64 in map[string]interface{}")
	assert.False(t, isUint64, "SendPaymentSync's `.(uint64)` type assertion cannot match a wire-supplied value")

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)
	txn, payErr := txnSvc.SendPaymentSync(tests.MockInvoice, nil, wireMetadata, svc.LNClient, &app.ID, nil)
	require.NoError(t, payErr)
	require.NotNil(t, txn)

	var stored db.Transaction
	require.NoError(t, svc.DB.Where("payment_hash = ? AND app_id = ?", tests.MockPaymentHash, app.ID).First(&stored).Error)
	assert.Nil(t, stored.CashRedeemFeeMloki,
		"today, a wire-supplied cash_redeem_fee_mloki is silently dropped by a type-assertion mismatch — not by an explicit strip")

	var reconciliationCount int64
	require.NoError(t, svc.DB.Model(&db.Transaction{}).
		Where("description = ?", "Cash redeem fee reconciliation").
		Count(&reconciliationCount).Error)
	assert.Zero(t, reconciliationCount, "no reconciliation fired for this ordinary pay_invoice payment")
}
