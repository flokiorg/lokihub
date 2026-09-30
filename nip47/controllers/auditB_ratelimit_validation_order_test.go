package controllers

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// TestAuditB_MalformedRedeemsDoNotSpendTheSharedBudget pins the ORDER of the
// rate-limit charge against param validation.
//
// A bill's redeem allowance is per BILL, not per caller — the connection is shared
// by every recipient, and the limiter is deliberately keyed on it because a
// cash-mode redemption has no signature to forge, only a secret to guess. That
// design is sound and unchanged.
//
// What broke it was batching. The charge sat before validation, which was
// per-request when a request was one message; the private transport made one
// envelope carry up to 32 items, each dispatched into this controller separately.
// So a co-recipient — someone who legitimately holds the token and passes the
// possession gate — could send a single envelope of malformed redeems, spend the
// whole bill's hourly allowance, and leave every other recipient RATE_LIMITED until
// it reset. Repeatable each hour until the bill expired, at which point the money is
// unreachable. The attacker needed no valid data at all.
//
// The fix charges only once a request is a real attempt. This test drives the
// controller directly rather than through an envelope, because the defect is the
// ordering inside it — the envelope is only what made 32 of them cheap.
//
// Against the pre-fix code the final redeem is refused RATE_LIMITED.
func TestAuditB_MalformedRedeemsDoNotSpendTheSharedBudget(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	budget := svc.Cfg.GetEnv().CashWalletClaimRateLimitPerHour
	require.Positive(t, budget,
		"this test is meaningless with the limiter disabled — it would pass without ever charging")

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	secretHex, secretHash := cashSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityCash, IdentityValue: secretHash, AmountMloki: 1000},
	}))

	// ONE controller for every call below, deliberately. NewTestNip47Controller builds
	// a fresh NewRateLimiter() each time it is called, so constructing it inside the
	// loop would hand every request an empty limiter and the test would pass without
	// ever charging anything. That is not hypothetical: the first draft of this file
	// did exactly that, and only the companion test — which asserts the limiter DOES
	// still fire — caught it.
	controller := NewTestNip47Controller(svc)

	// Enough malformed redeems to exhaust the allowance several times over. Each is
	// rejected on its shape: an empty invoice can never redeem anything, so none of
	// these was ever a real attempt on the secret.
	for i := 0; i < budget*2; i++ {
		resp := handleClaimFundsFor(t, svc, controller, wallet, nipcash.CashRedeemRequest{
			Invoice:    "",
			CashSecret: secretHex,
		})
		require.NotNil(t, resp.Error, "a malformed redeem must be refused")
		require.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code,
			"refused for its SHAPE, not its rate — if this is RATE_LIMITED the charge still "+
				"precedes validation and the bug is present (attempt %d of %d)", i+1, budget*2)
	}

	// The load-bearing assertion: a real recipient's genuine redeem still works.
	resp := handleClaimFundsFor(t, svc, controller, wallet, nipcash.CashRedeemRequest{
		Invoice:    tests.MockZeroAmountInvoice,
		Amount:     ptrUint64(1000),
		CashSecret: secretHex,
	})
	require.Nil(t, resp.Error,
		"%d malformed requests spent the bill's whole hourly allowance, so a co-recipient's "+
			"genuine redeem is now refused — one envelope can carry 32 of these, and the bill "+
			"stays locked out until the window resets", budget*2)
}

// TestAuditB_RealAttemptsStillSpendTheBudget is the other half, and the reason the
// fix is a reorder rather than a removal: a well-formed redeem carrying a WRONG
// secret is exactly what a brute-force attempt looks like, and it must still be
// charged. Moving the limiter after validation must not have made guessing free.
func TestAuditB_RealAttemptsStillSpendTheBudget(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	budget := svc.Cfg.GetEnv().CashWalletClaimRateLimitPerHour
	require.Positive(t, budget)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	_, realHash := cashSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityCash, IdentityValue: realHash, AmountMloki: 1000},
	}))

	// ONE controller for every call below, deliberately. NewTestNip47Controller builds
	// a fresh NewRateLimiter() each time it is called, so constructing it inside the
	// loop would hand every request an empty limiter and the test would pass without
	// ever charging anything. That is not hypothetical: the first draft of this file
	// did exactly that, and only the companion test — which asserts the limiter DOES
	// still fire — caught it.
	controller := NewTestNip47Controller(svc)

	// Well-formed guesses at the secret. Each is a genuine attempt and must be charged.
	sawRateLimit := false
	for i := 0; i < budget+5; i++ {
		guessHex, _ := cashSecretAndHash(t)
		resp := handleClaimFundsFor(t, svc, controller, wallet, nipcash.CashRedeemRequest{
			Invoice:    tests.MockZeroAmountInvoice,
			Amount:     ptrUint64(1000),
			CashSecret: guessHex,
		})
		require.NotNil(t, resp.Error, "a wrong secret must never redeem")
		if resp.Error.Code == constants.ERROR_RATE_LIMITED {
			sawRateLimit = true
			break
		}
	}
	require.True(t, sawRateLimit,
		"guessing the secret must still exhaust the allowance — if it does not, the reorder "+
			"removed the only throttle standing between a cash-mode slice and a brute force")
}
