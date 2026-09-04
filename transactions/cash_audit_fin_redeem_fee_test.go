package transactions

// Financial/economic design review (2026-08-02) — Cash Hub / lokicash.
//
// This test isolates a purely economic property of the shared-pool funding
// model, not a security bug: a cash_wallet is funded with EXACTLY the sum of
// its recipients' slices (cashwallet.Commit / NIP-CASH "total funding MUST
// equal the sum of its slices"), leaving zero headroom for the Lightning
// routing fee an external redemption actually costs. Every redemption's
// fee_mloki is subtracted from that ONE shared isolated balance
// (queries.GetIsolatedBalance sums amount_mloki + fee_mloki + ... for outgoing
// txns of the app). So in a MULTI-recipient wallet, the fee an earlier
// recipient pays to redeem out to an external node is drawn from the same pool
// that still has to back every later recipient's slice — and cash_redeem's
// exact-amount rule (invoice amount == the slice, to the mloki) means a later
// recipient whose slice is no longer fully backed cannot redeem at all. The
// last recipient bears the accumulated fees of everyone before them, and if
// the shortfall pushes their backing below their slice, their value is stranded
// until expiry sweeps it back to the hub.

import (
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func auditRandHex32() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// newFundedCashWallet inserts a cash_wallet app funded with fundedMloki (one
// settled incoming transfer, the way cashwallet.Commit funds it), plus a
// CASH_REDEEM_SCOPE permission whose budget cap equals the funded sum in loki
// — mirroring the real wallet exactly (budget == funded == sum of slices).
func newFundedCashWallet(t *testing.T, svc *tests.TestService, fundedMloki int64) *db.App {
	t.Helper()
	app := &db.App{
		Name:       "cash-wallet",
		AppPubkey:  auditRandHex32(),
		Kind:       db.AppKindCashWallet,
		ParentKind: db.ParentKindCash,
	}
	require.NoError(t, svc.DB.Create(app).Error)
	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId:         app.ID,
		Scope:         constants.CASH_REDEEM_SCOPE,
		MaxAmountLoki: int(fundedMloki / 1000),
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
	}).Error)
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:       &app.ID,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_SETTLED,
		AmountMloki: uint64(fundedMloki), //nolint:gosec // test-controlled positive value
		PaymentHash: auditRandHex32(),
	}).Error)
	return app
}

// FINDING (High): the last recipient of a multi-recipient cash_wallet can be
// permanently unable to redeem their own, legitimately-owned slice, because an
// earlier co-recipient's external-redemption routing fee was subtracted from
// the shared pool that still had to back it. No theft, no double-spend — the
// central recipient-facing promise ("you can always collect your exact slice")
// simply fails to hold for the system's headline use case (a payout split
// across many people who redeem out to real Lightning wallets over time).
func TestCashAuditFin_MultiRecipientRedeemFee_StrandsLastSlice(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Two recipients, 123000 mloki each — the second slice's amount is chosen
	// to match the mock invoice the payment path decodes (123000 mloki), so
	// this exercises the real redemption code path, not a hand-rolled amount.
	const a1 = int64(123000)
	const a2 = int64(123000)
	const fee1 = int64(500) // R1's real external routing fee — half a loki
	funded := a1 + a2       // exactly the sum of the two slices

	wallet := newFundedCashWallet(t, svc, funded)

	// Recipient 1 has already redeemed out to an external node: an outgoing
	// SETTLED payment of a1 that also cost a real routing fee of fee1.
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:       &wallet.ID,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		State:       constants.TRANSACTION_STATE_SETTLED,
		AmountMloki: uint64(a1), //nolint:gosec // test-controlled positive value
		FeeMloki:    uint64(fee1),
		PaymentHash: auditRandHex32(),
	}).Error)

	// The shared pool is now short by exactly R1's fee: it holds a2 - fee1,
	// which is LESS than the a2 that still has to back recipient 2's slice.
	balance := queries.GetIsolatedBalance(svc.DB, wallet.ID)
	assert.Equal(t, a2-fee1, balance,
		"an earlier recipient's routing fee is drawn from the shared pool that backs the remaining slices")
	assert.Less(t, balance, a2,
		"the pool no longer fully backs recipient 2's own, un-redeemed slice")

	// Recipient 2 now presents a fresh invoice for EXACTLY their slice (a2 =
	// 123000, the mock invoice). This is the exact code path cash_redeem's
	// step 10 takes: SendPaymentSync with the cash_claim_slice flag (which
	// waives the fee RESERVE pre-check, but NOT the hard isolated-balance
	// check). It must fail for insufficient balance — recipient 2 cannot
	// collect their own slice.
	reqEvent := &db.RequestEvent{NostrId: auditRandHex32()}
	require.NoError(t, svc.DB.Create(reqEvent).Error)

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)
	txn, payErr := txnSvc.SendPaymentSync(
		tests.MockLNClientTransaction.Invoice, nil,
		map[string]interface{}{"cash_claim_slice": true},
		svc.LNClient, &wallet.ID, &reqEvent.ID,
	)
	require.Error(t, payErr, "recipient 2's exact-slice redemption must not succeed against an under-backed pool")
	assert.ErrorIs(t, payErr, NewInsufficientBalanceError())
	assert.Nil(t, txn)
}

// CONTRAST / CONFIRMED SOUND: a SINGLE-recipient wallet redeeming its whole
// slice passes the pre-check (balance == slice, no prior co-recipient fee has
// eaten into it). The operator absorbs that redemption's own fee out of the
// node's global balance — which is the intended "cash" model. The problem
// above is specific to the SHARED pool of a multi-recipient wallet, where one
// recipient's fee is silently charged against another recipient's backing.
func TestCashAuditFin_SingleRecipientRedeem_PreCheckPasses(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// One recipient, funded with exactly their slice (123000 = mock invoice).
	wallet := newFundedCashWallet(t, svc, 123000)

	balance := queries.GetIsolatedBalance(svc.DB, wallet.ID)
	require.Equal(t, int64(123000), balance)

	reqEvent := &db.RequestEvent{NostrId: auditRandHex32()}
	require.NoError(t, svc.DB.Create(reqEvent).Error)

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)
	txn, payErr := txnSvc.SendPaymentSync(
		tests.MockLNClientTransaction.Invoice, nil,
		map[string]interface{}{"cash_claim_slice": true},
		svc.LNClient, &wallet.ID, &reqEvent.ID,
	)
	require.NoError(t, payErr, "a single recipient redeeming their whole, fully-backed slice must succeed")
	require.NotNil(t, txn)
	assert.Equal(t, uint64(123000), txn.AmountMloki)
}

// FIX CONFIRMATION: TestCashAuditFin_MultiRecipientRedeemFee_FixedByRedeemFeePpm
// is the direct counterpart to the stranding finding above, replaying the
// identical two-recipient scenario (same a1/a2/fee1 values) but with a
// nonzero redeem_fee_ppm configured on recipient 1's slice, comfortably
// covering their real routing fee (fee1). Under the fix, R1's fee comes out
// of R1's OWN payout (deducted from the invoice they present) and is
// reconciled against the Cash Hub at settlement (reconcileCashRedeemFee),
// never against the shared pool — so the pool is left backing EXACTLY a2
// again, and recipient 2's own, unrelated slice — which failed outright in
// the finding above — now redeems successfully.
func TestCashAuditFin_MultiRecipientRedeemFee_FixedByRedeemFeePpm(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const a1 = int64(123000)
	const a2 = int64(123000)
	const fee1 = int64(500)        // R1's real external routing fee — identical to the stranding scenario
	const quotedFee1 = int64(1000) // the hub's redeem_fee_ppm quote for R1's slice, comfortably covering fee1
	funded := a1 + a2

	hub := &db.App{Name: "cash-hub", AppPubkey: auditRandHex32(), Kind: db.AppKindCashHub}
	require.NoError(t, svc.DB.Create(hub).Error)

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
		// MaxAmountLoki left at its zero value (unbounded) — this test isolates
		// the shared-pool balance invariant from unrelated quota math.
	}).Error)
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:       &wallet.ID,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_SETTLED,
		AmountMloki: uint64(funded), //nolint:gosec // test-controlled positive value
		PaymentHash: auditRandHex32(),
	}).Error)

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)

	// Recipient 1 redeems externally for the NET amount (a1 - quotedFee1) —
	// tests.MockInvoice's own amount is fixed at a1, so the amountless
	// tests.MockZeroAmountInvoice is used instead, with an explicit override,
	// to express a net-of-fee amount.
	mockLn := svc.LNClient.(*tests.MockLn)
	mockLn.PayInvoiceResponses = append(mockLn.PayInvoiceResponses, &lnclient.PayInvoiceResponse{
		Preimage: "r1-external-preimage",
		Fee:      uint64(fee1), //nolint:gosec // test-controlled positive value
	})
	mockLn.PayInvoiceErrors = append(mockLn.PayInvoiceErrors, nil)

	netR1 := uint64(a1 - quotedFee1) //nolint:gosec // test-controlled positive value
	txn1, err := txnSvc.SendPaymentSync(
		tests.MockZeroAmountInvoice, &netR1,
		map[string]interface{}{"cash_claim_slice": true, "cash_redeem_fee_mloki": uint64(quotedFee1)}, //nolint:gosec // test-controlled positive value
		svc.LNClient, &wallet.ID, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, txn1)

	// The pool is backed by EXACTLY a2 again: R1's real fee (500) came out of
	// R1's own quoted fee (1000, reconciled as a 500 mloki surplus to the
	// hub), never out of the shared pool that still has to cover R2.
	balance := queries.GetIsolatedBalance(svc.DB, wallet.ID)
	assert.Equal(t, a2, balance,
		"with the fix, recipient 1's fee no longer strands recipient 2's own, unrelated slice")

	// Recipient 2 now presents a fresh invoice for EXACTLY their own slice
	// (no fee configured on their claim) — the exact call that failed with
	// NewInsufficientBalanceError in the finding this test fixes.
	reqEvent := &db.RequestEvent{NostrId: auditRandHex32()}
	require.NoError(t, svc.DB.Create(reqEvent).Error)

	txn2, payErr := txnSvc.SendPaymentSync(
		tests.MockInvoice, nil,
		map[string]interface{}{"cash_claim_slice": true, "cash_redeem_fee_mloki": uint64(0)},
		svc.LNClient, &wallet.ID, &reqEvent.ID,
	)
	require.NoError(t, payErr, "recipient 2's exact-slice redemption must now succeed")
	require.NotNil(t, txn2)
	assert.Equal(t, uint64(a2), txn2.AmountMloki) //nolint:gosec // test-controlled positive value

	assert.Equal(t, int64(0), queries.GetIsolatedBalance(svc.DB, wallet.ID),
		"both recipients collected exactly their own slices — the wallet is fully, correctly drained")
}
