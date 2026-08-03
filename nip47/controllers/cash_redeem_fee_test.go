package controllers

// Test coverage for cash_redeem's fee-aware exact-match rule (step 9 of
// HandleCashRedeemEvent) — a slice carrying a nonzero RedeemFeePpm must be
// redeemed for exactly its net (fee-reduced) amount when the redemption
// resolves to a genuine external payment, and for its full, fee-free amount
// when it resolves to a same-node one (transactions.IsSelfPayment). See
// NIP-CASH.md's §The Redeem Fee and cash_redeem_controller.go step 9's own
// doc comment.

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// mockZeroAmountInvoicePayeePubkey is tests.MockZeroAmountInvoice's own
// decoded payee pubkey — setting MockLn.Pubkey to this value makes
// transactions.IsSelfPayment resolve this exact invoice as same-node,
// provided a matching incoming db.Transaction row also exists.
const mockZeroAmountInvoicePayeePubkey = "03a078ec02e002be52c961b3fcc3c0d92f096b8a86844e256ad6f03aadbbc703ce"

func TestHandleCashRedeemEvent_ExternalRedemption_FeeDeductedFromPayout(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	// 10% redeem fee on a 1000 mloki slice: fee 100, net redeemable 900.
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000, RedeemFeePpm: 100_000},
	}))

	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())

	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, cashRedeemParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(900), // the slice's net redeemable amount, not its full 1000
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})

	require.Nil(t, response.Error)
	result := response.Result.(payResponse)
	assert.Equal(t, uint64(100), result.FeesPaid, "FeesPaid must report the hub's own redeem fee, not the (fee-free, mocked) real routing fee")

	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, claimantPubkey)
	require.NoError(t, err)
	assert.Nil(t, claim, "slice must show as claimed")
}

func TestHandleCashRedeemEvent_ExternalRedemption_FullSliceAmount_RejectedWhenFeeApplies(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000, RedeemFeePpm: 100_000},
	}))

	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())

	// Presenting an invoice for the FULL slice, ignoring the fee, must be
	// rejected — an external redemption never pays out more than the net.
	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, cashRedeemParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000),
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, claimantPubkey)
	require.NoError(t, err)
	require.NotNil(t, claim, "a rejected attempt must not burn the slice")
}

func TestHandleCashRedeemEvent_ExternalRedemption_BelowQuote_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000, RedeemFeePpm: 100_000},
	}))

	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())

	// 800 is neither the full slice (1000) nor the correctly-quoted net (900).
	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, cashRedeemParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(800),
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, claimantPubkey)
	require.NoError(t, err)
	require.NotNil(t, claim)
}

// TestHandleCashRedeemEvent_SameNodeRedemption_FeeFree_FullAmountPaid proves
// the same-node exemption: even though the claim's own RedeemFeePpm is
// nonzero, a redemption transactions.IsSelfPayment determines resolves to a
// same-node payment must pay out the FULL slice, with zero fee.
func TestHandleCashRedeemEvent_SameNodeRedemption_FeeFree_FullAmountPaid(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).Pubkey = mockZeroAmountInvoicePayeePubkey

	mockPreimage := "same-node-preimage"
	require.NoError(t, svc.DB.Create(&db.Transaction{
		State:          constants.TRANSACTION_STATE_PENDING,
		Type:           constants.TRANSACTION_TYPE_INCOMING,
		PaymentRequest: tests.MockZeroAmountInvoice,
		PaymentHash:    tests.MockZeroAmountPaymentHash,
		Preimage:       &mockPreimage,
		AmountMloki:    1000,
	}).Error)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	// A 10% fee is configured on this slice — it must still be waived, since
	// this specific redemption resolves to a same-node payment.
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000, RedeemFeePpm: 100_000},
	}))

	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())

	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, cashRedeemParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000), // the FULL slice — must succeed, fee-free
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})

	require.Nil(t, response.Error)
	result := response.Result.(payResponse)
	assert.Zero(t, result.FeesPaid, "a same-node redemption must be fee-free even though the claim's RedeemFeePpm is nonzero")
}

// TestHandleCashRedeemEvent_ZeroRedeemFeePpm_FullAmountEitherWay confirms the
// zero-value default (every pre-existing claim, or a Hub that never
// configured a rate) behaves exactly like before this feature existed:
// the full slice, no fee, for an ordinary external redemption too.
func TestHandleCashRedeemEvent_ZeroRedeemFeePpm_FullAmountEitherWay(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000}, // RedeemFeePpm defaults to 0
	}))

	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())

	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, cashRedeemParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000),
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})

	require.Nil(t, response.Error)
	result := response.Result.(payResponse)
	assert.Zero(t, result.FeesPaid)
}
