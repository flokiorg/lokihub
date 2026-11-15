package transactions

// Test coverage for the redeem-fee reconciliation mechanism introduced
// alongside db.CashWalletClaim.RedeemFeePpm (see reconcileCashRedeemFee's own
// doc comment in transactions_service.go, and NIP-CASH.md's §The Redeem Fee).
//
// These tests drive SendPaymentSync directly, the same way
// cash_audit_fin_redeem_fee_test.go does, setting the cash_claim_slice and
// cash_redeem_fee_mloki metadata flags cash_redeem_controller.go itself sets
// after computing the quoted fee — this isolates the settlement-time
// reconciliation from the rest of cash_redeem's proof/identity machinery,
// which nip47/controllers' own cash_redeem_fee_test.go covers separately.
//
// Every case below asserts the SAME invariant, proven in NIP-CASH.md:
// (net + real) + delta == claimed, i.e. the wallet's total isolated-balance
// debit for one redemption is always exactly the slice's own committed
// amount — never more, regardless of how the quoted fee compares to the real
// Lightning routing cost. Each wallet is funded with `claimed` (net +
// quotedFee), mirroring NIP-CASH's "total funding MUST equal the sum of its
// slices" — the same way a real cash_wallet holds the full, not fee-reduced,
// slice amount before redemption.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// newCashHubAndWallet creates a real cash_hub parent and a cash_wallet child
// funded with claimedMloki — the slice's full committed amount, the way a
// real cash_wallet is funded before redemption — so reconcileCashRedeemFee
// has a real ParentAppID to settle its wallet<->hub adjustment against,
// unlike cash_audit_fin_redeem_fee_test.go's newFundedCashWallet, which
// deliberately has no parent at all.
func newCashHubAndWallet(t *testing.T, svc *tests.TestService, claimedMloki uint64) (*db.App, *db.App) {
	t.Helper()
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
	// MaxAmountLoki left at its zero value (unbounded budget) — these tests
	// isolate the reconciliation invariant from unrelated quota math.
	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId: wallet.ID,
		Scope: constants.CASH_REDEEM_SCOPE,
	}).Error)
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:       &wallet.ID,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_SETTLED,
		AmountMloki: claimedMloki,
		PaymentHash: auditRandHex32(),
	}).Error)
	return hub, wallet
}

// redeemMetadata builds the metadata cash_redeem_controller.go itself sends
// to SendPaymentSync after computing quotedFeeMloki from the claim's own
// RedeemFeePpm — see cash_redeem_controller.go step 10.
func redeemMetadata(quotedFeeMloki uint64) map[string]interface{} {
	return map[string]interface{}{
		"cash_claim_slice":      true,
		"cash_redeem_fee_mloki": quotedFeeMloki,
	}
}

func TestSendPaymentSync_CashRedeemFee_QuotedExceedsReal_WalletPaysHubSurplus(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const net = uint64(123_000) // tests.MockInvoice's own fixed amount — what the invoice actually pays out
	const quotedFee = uint64(500)
	const claimed = net + quotedFee

	hub, wallet := newCashHubAndWallet(t, svc, claimed)
	// Real routing fee defaults to 0 (MockLn.SendPaymentSync's zero-value
	// response) — the quoted fee covers the whole real cost, plus a surplus.

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := txnSvc.SendPaymentSync(
		tests.MockInvoice, nil, redeemMetadata(quotedFee), svc.LNClient, &wallet.ID, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, transaction)

	assert.Equal(t, int64(0), queries.GetIsolatedBalance(svc.DB, wallet.ID),
		"the wallet's isolated balance must decrease by EXACTLY the redeemed slice's claimed amount")
	assert.Equal(t, int64(quotedFee), queries.GetIsolatedBalance(svc.DB, hub.ID),
		"the hub nets the full quoted fee as revenue when it exceeds the real routing cost")
}

func TestSendPaymentSync_CashRedeemFee_RealExceedsQuoted_HubReimbursesShortfall(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const net = uint64(123_000)
	const quotedFee = uint64(100)
	const realFee = uint64(300)
	const claimed = net + quotedFee

	hub, wallet := newCashHubAndWallet(t, svc, claimed)

	mockLn := svc.LNClient.(*tests.MockLn)
	mockLn.PayInvoiceResponses = append(mockLn.PayInvoiceResponses, &lnclient.PayInvoiceResponse{
		Preimage: "real-fee-preimage",
		Fee:      realFee,
	})
	mockLn.PayInvoiceErrors = append(mockLn.PayInvoiceErrors, nil)

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := txnSvc.SendPaymentSync(
		tests.MockInvoice, nil, redeemMetadata(quotedFee), svc.LNClient, &wallet.ID, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, transaction)
	assert.Equal(t, realFee, transaction.FeeMloki)

	assert.Equal(t, int64(0), queries.GetIsolatedBalance(svc.DB, wallet.ID),
		"the wallet's isolated balance must decrease by EXACTLY the redeemed slice's claimed amount, even though the real routing cost exceeded the quote")
	assert.Equal(t, -int64(realFee-quotedFee), queries.GetIsolatedBalance(svc.DB, hub.ID),
		"the hub absorbs exactly the shortfall (300 real cost - 100 quoted fee = 200 reimbursed to the wallet) as a loss, never at the wallet's expense")
}

func TestSendPaymentSync_CashRedeemFee_QuotedEqualsReal_NoReconciliationRows(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const net = uint64(123_000)
	const fee = uint64(200)
	const claimed = net + fee

	hub, wallet := newCashHubAndWallet(t, svc, claimed)

	mockLn := svc.LNClient.(*tests.MockLn)
	mockLn.PayInvoiceResponses = append(mockLn.PayInvoiceResponses, &lnclient.PayInvoiceResponse{
		Preimage: "exact-fee-preimage",
		Fee:      fee,
	})
	mockLn.PayInvoiceErrors = append(mockLn.PayInvoiceErrors, nil)

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := txnSvc.SendPaymentSync(
		tests.MockInvoice, nil, redeemMetadata(fee), svc.LNClient, &wallet.ID, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, transaction)

	assert.Equal(t, int64(0), queries.GetIsolatedBalance(svc.DB, wallet.ID))
	assert.Zero(t, queries.GetIsolatedBalance(svc.DB, hub.ID),
		"delta == 0 must create no synthetic reconciliation rows at all")

	var count int64
	svc.DB.Model(&db.Transaction{}).Where("app_id = ?", hub.ID).Count(&count)
	assert.Zero(t, count, "no reconciliation row should ever land on the hub when delta is exactly zero")
}

// TestSendPaymentSync_CashRedeemFee_SameNodeRedemption_ZeroQuoteZeroReal_NoOp
// covers the same-node case: cash_redeem_controller.go always quotes a zero
// fee for a redemption it determined (via transactions.IsSelfPayment) will
// resolve to a same-node payment, and a same-node payment's real fee is
// always 0 too (interceptSelfPayment never touches the real Lightning
// network) — so this is the delta==0 case, but arrived at via the actual
// self-payment path rather than a hand-set mock fee.
func TestSendPaymentSync_CashRedeemFee_SameNodeRedemption_ZeroQuoteZeroReal_NoOp(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"

	const claimed = uint64(123_000) // tests.MockInvoice's own fixed amount == the full slice, fee-free
	hub, wallet := newCashHubAndWallet(t, svc, claimed)

	mockPreimage := "same-node-preimage"
	require.NoError(t, svc.DB.Create(&db.Transaction{
		State:          constants.TRANSACTION_STATE_PENDING,
		Type:           constants.TRANSACTION_TYPE_INCOMING,
		PaymentRequest: tests.MockInvoice,
		PaymentHash:    tests.MockPaymentHash,
		Preimage:       &mockPreimage,
		AmountMloki:    claimed,
	}).Error)

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := txnSvc.SendPaymentSync(
		tests.MockInvoice, nil, redeemMetadata(0), svc.LNClient, &wallet.ID, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, transaction)
	assert.True(t, transaction.SelfPayment)
	assert.Zero(t, transaction.FeeMloki)

	assert.Equal(t, int64(0), queries.GetIsolatedBalance(svc.DB, wallet.ID))
	assert.Zero(t, queries.GetIsolatedBalance(svc.DB, hub.ID),
		"a same-node redemption must never move anything into or out of the hub's own balance")

	var count int64
	svc.DB.Model(&db.Transaction{}).Where("app_id = ?", hub.ID).Count(&count)
	assert.Zero(t, count)
}

// TestSendPaymentSync_CashRedeemFee_ReconciliationMetadataTagged asserts the
// debit/credit pair reconcileCashRedeemFee creates carries the operator-
// debuggability tags its own doc comment promises, mirroring circle_fee_skim_
// test.go's equivalent assertion for creditCircleHubFeeSkim.
func TestSendPaymentSync_CashRedeemFee_ReconciliationMetadataTagged(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const net = uint64(123_000)
	const quotedFee = uint64(500)
	const claimed = net + quotedFee

	_, wallet := newCashHubAndWallet(t, svc, claimed)

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := txnSvc.SendPaymentSync(
		tests.MockInvoice, nil, redeemMetadata(quotedFee), svc.LNClient, &wallet.ID, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, transaction)

	var walletDebit db.Transaction
	require.NoError(t, svc.DB.Where("app_id = ? AND type = ?", wallet.ID, constants.TRANSACTION_TYPE_OUTGOING).
		Order("id desc").First(&walletDebit).Error)
	assert.NotEqual(t, tests.MockPaymentHash, walletDebit.PaymentHash,
		"the reconciliation debit must be a distinct synthetic row, not the payout's own transaction")

	var metadata map[string]interface{}
	require.NoError(t, json.Unmarshal(walletDebit.Metadata, &metadata))
	assert.EqualValues(t, wallet.ID, metadata["cash_redeem_fee_source_app_id"])
	assert.Equal(t, tests.MockPaymentHash, metadata["cash_redeem_fee_source_payment_hash"])
	assert.EqualValues(t, quotedFee, metadata["cash_redeem_hub_fee_mloki"])
	assert.EqualValues(t, 0, metadata["cash_redeem_real_fee_mloki"])
	assert.EqualValues(t, quotedFee, metadata["cash_redeem_delta_mloki"])
}
