package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// TestAuditB_MalformedRedeemsDoNotSpendTheSharedBudget pins where the call limit
// is charged: only once a call is authorized, keyed by its verified signer or
// commitment. A co-recipient sending an envelope of malformed redeems against a
// shared bill must never spend anyone's allowance, so every one of them is refused
// for its shape and a genuine redeem afterwards still works.
func TestAuditB_MalformedRedeemsDoNotSpendTheSharedBudget(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	budget := svc.Cfg.GetEnv().CashClaimCallsPerHour
	require.Positive(t, budget,
		"this test is meaningless with the limiter disabled — it would pass without ever charging")

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	secretHex, secretHash := cashSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityCash, IdentityValue: secretHash, AmountMloki: 1000},
	}))

	// ONE controller, so one limiter, for every call below.
	controller := NewTestNip47Controller(svc)

	for i := 0; i < budget*2; i++ {
		resp := handleClaimFundsFor(t, svc, controller, wallet, nipcash.CashRedeemRequest{
			Invoice:    "",
			CashSecret: secretHex,
		})
		require.NotNil(t, resp.Error, "a malformed redeem must be refused")
		require.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code,
			"refused for its SHAPE, not its rate (attempt %d of %d)", i+1, budget*2)
	}

	resp := handleClaimFundsFor(t, svc, controller, wallet, nipcash.CashRedeemRequest{
		Invoice:    tests.MockZeroAmountInvoice,
		Amount:     ptrUint64(1000),
		CashSecret: secretHex,
	})
	require.Nil(t, resp.Error, "malformed requests spent the allowance of a genuine redeem")
}

// TestAuditB_CallLimitKeyedByCommitmentNotBill (U-8.4): authorized calls are
// charged to the slice's own commitment. Once one holder's allowance is spent its
// next call is RATE_LIMITED, while a co-recipient of the same bill is untouched.
func TestAuditB_CallLimitKeyedByCommitmentNotBill(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	budget := svc.Cfg.GetEnv().CashClaimCallsPerHour
	require.Positive(t, budget)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 2000)

	secretHex, secretHash := cashSecretAndHash(t)
	coPriv := nostr.GeneratePrivateKey()
	coPub, _ := nostr.GetPublicKey(coPriv)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityCash, IdentityValue: secretHash, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: coPub, AmountMloki: 1000},
	}))
	controller := NewTestNip47Controller(svc)

	// Authorized, then refused on the amount: each one is charged.
	for i := 0; i < budget; i++ {
		resp := handleClaimFundsFor(t, svc, controller, wallet, nipcash.CashRedeemRequest{
			Invoice: tests.MockZeroAmountInvoice, Amount: ptrUint64(999), CashSecret: secretHex,
		})
		require.NotNil(t, resp.Error)
		require.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code, "attempt %d", i+1)
	}
	resp := handleClaimFundsFor(t, svc, controller, wallet, nipcash.CashRedeemRequest{
		Invoice: tests.MockZeroAmountInvoice, Amount: ptrUint64(1000), CashSecret: secretHex,
	})
	require.NotNil(t, resp.Error)
	require.Equal(t, constants.ERROR_RATE_LIMITED, resp.Error.Code)

	// A wrong secret is refused UNAUTHORIZED and never charged to anyone.
	guessHex, _ := cashSecretAndHash(t)
	resp = handleClaimFundsFor(t, svc, controller, wallet, nipcash.CashRedeemRequest{
		Invoice: tests.MockZeroAmountInvoice, Amount: ptrUint64(1000), CashSecret: guessHex,
	})
	require.NotNil(t, resp.Error)
	require.Equal(t, constants.ERROR_UNAUTHORIZED, resp.Error.Code)

	// The co-recipient of the same bill still redeems.
	proof := buildClaimProofEvent(t, coPriv, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())
	resp = handleClaimFundsFor(t, svc, controller, wallet, nipcash.CashRedeemRequest{
		Invoice: tests.MockZeroAmountInvoice, Amount: ptrUint64(1000),
		IdentityType: db.CashIdentityPubkey, IdentityValue: coPub, IdentityEvent: mustMarshal(t, proof),
	})
	require.Nil(t, resp.Error, "a limit spent by one holder must never refuse another recipient of the bill")
}
