package transactions

// Independent security audit A (2026-08-02) — Cash Hub redeem-fee mechanism.
//
// CHECKED, FOUND SOUND (documenting test, not a finding): cash_redeem_controller.go
// step 9 peeks transactions.IsSelfPayment(controller.db, ...) BEFORE calling
// SendPaymentSync, which independently calls the exact same
// IsSelfPayment(svc.db, ...) a few lines later (transactions_service.go:407),
// outside any shared transaction or lock. This test asks the question the
// audit brief calls out directly: can these two, non-atomic reads of the same
// predicate disagree, opening a fee-bypass (peek says external, real
// determination says same-node — recipient overcharged, Hub pockets a fee it
// never should have) or fee-overcharge (peek says same-node, real
// determination says external — Hub silently eats a real Lightning fee it
// was owed) window?
//
// IsSelfPayment's own query (transactions_service.go:1249-1259) matches on
// Type+PaymentHash ONLY — it does not filter by State, and this codebase
// never deletes a db.Transaction row (grep confirms no `Delete` on the
// transactions table outside of these audit tests' own fixtures). So once a
// matching incoming row exists, IsSelfPayment returns true for that payment
// hash forever after, regardless of what happens to the row's State — the
// peek and the real check can never disagree in the "was true, now false"
// direction. And the reverse ("was false, now true") direction requires a
// NEW incoming row appearing between the two reads for the EXACT payment
// hash embedded in the invoice the caller already possesses — but
// MakeInvoice (transactions_service.go:210-318) commits that row via
// svc.db.Create BEFORE ever returning the bolt11 string to any caller, so a
// legitimate caller can never be holding an invoice whose backing row isn't
// already committed by the time they present it to cash_redeem.
//
// What CAN happen: IsSelfPayment's own "any state" leniency means it reports
// true even for a matching incoming row that is no longer PENDING (already
// settled some other way, or expired/failed) — but the code that actually
// ACTS on selfPayment==true, interceptSelfPayment
// (transactions_service.go:1261-1303), requires State==PENDING and returns
// NewNotFoundError() otherwise. This test proves that mismatch fails CLOSED,
// not open: the payment attempt errors out, markPaymentFailed marks the
// pending outgoing row FAILED (never SETTLED), no funds move, and (in the
// real cash_redeem_controller.go flow, not exercised at this
// transactions-package layer) the caller's slice claim gets rolled back via
// UnclaimCashSlice. No fee bypass, no double-spend, no stranded claim.
import (
	"strings"
	"testing"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	decodepay "github.com/flokiorg/lokihub/decodepay"
	"github.com/flokiorg/lokihub/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockInvoicePayeePubkey is tests.MockInvoice's own embedded payee pubkey —
// the same value TestSecA_BearerInPlace_RedeemedCoRecipientStillCounted
// (nip47/controllers/cash_audit_secA_lifetime_solo_test.go) sets as
// mockLN.Pubkey to make tests.MockInvoice resolve as a same-node payment.
const mockInvoicePayeePubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"

func TestCashAuditRedeemFeeSecA_SelfPaymentPeekVsInterception_MismatchFailsClosed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	mockLn := svc.LNClient.(*tests.MockLn)
	mockLn.Pubkey = mockInvoicePayeePubkey

	paymentRequest, err := decodepay.Decode(strings.ToLower(tests.MockInvoice))
	require.NoError(t, err)
	require.Equal(t, mockInvoicePayeePubkey, paymentRequest.Payee, "sanity: MockInvoice's payee matches the mocked node pubkey")

	// A matching incoming row exists, but it is FAILED, not PENDING — e.g. the
	// same-node target invoice's own receiving leg already expired or was
	// independently marked failed through some other path. IsSelfPayment
	// doesn't care about State, so the peek and the real check both still see
	// "self."
	require.NoError(t, svc.DB.Create(&db.Transaction{
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_FAILED,
		PaymentHash: paymentRequest.PaymentHash,
		AmountMloki: 123000,
	}).Error)

	// The peek (cash_redeem_controller.go step 9's own call) says self-payment.
	assert.True(t, IsSelfPayment(svc.DB, paymentRequest, svc.LNClient),
		"IsSelfPayment matches on Type+PaymentHash only, ignoring the row's actual State")

	payingApp, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId: payingApp.ID,
		Scope: constants.PAY_INVOICE_SCOPE,
	}).Error)

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)
	txn, payErr := txnSvc.SendPaymentSync(tests.MockInvoice, nil, nil, svc.LNClient, &payingApp.ID, nil)

	// Fails closed: SendPaymentSync's own selfPayment determination agrees
	// with the peek (also true, same query), takes the interceptSelfPayment
	// branch, and that branch's stricter PENDING-only requirement rejects it.
	require.Error(t, payErr, "a same-node redemption target whose incoming leg isn't PENDING must not silently succeed as a real external payment either")
	assert.Nil(t, txn)

	var stored db.Transaction
	require.NoError(t, svc.DB.Where(
		"payment_hash = ? AND type = ?", paymentRequest.PaymentHash, constants.TRANSACTION_TYPE_OUTGOING,
	).First(&stored).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, stored.State,
		"the outgoing attempt is marked FAILED, never SETTLED — no funds moved, no fee-bypass, no phantom payout")
}
