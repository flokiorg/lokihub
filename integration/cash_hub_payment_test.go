//go:build integration

// cash_hub_payment_test.go exercises real money movement using only a cash_hub's
// own wallet as the invoice source, rather than depending on a circle_hub
// being configured (see cross_test.go for the circle_hub-sourced equivalent).
// A cash_hub granted make_invoice scope in addition to its usual get_balance
// can mint a real invoice for one of its own freshly-minted Cash children to
// claim against.
package integration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// mintInvoiceFromHub mints a real invoice from hub's own NWC connection.
// Skips cleanly if the hub isn't granted make_invoice scope, mirroring the
// get_balance capability probe in cross_test.go.
func mintInvoiceFromHub(t *testing.T, hub CashHubConfig, amountMloki uint64) MakeInvoiceResult {
	t.Helper()
	hubClient := mustConnect(t, hub.Connection)

	var invoice MakeInvoiceResult
	err := hubClient.Call(ctxT(t), "make_invoice", MakeInvoiceParams{
		Amount:      amountMloki,
		Description: "integration cash_hub-sourced payment test",
	}, &invoice)
	if err != nil {
		t.Skipf("skipping: cash_hub %q isn't granted make_invoice scope (%v) - see integration/README.md", hub.Name, err)
	}
	require.NotEmpty(t, invoice.Invoice)
	return invoice
}

func TestCashHubPayments(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "cash-hub-payments", nil)
	testCashHubPayments(t, hub)
}

func testCashHubPayments(t *testing.T, hub CashHubConfig) {
	t.Run("ClaimFunds_CashChildClaimsHubMintedInvoice_HappyPath", func(t *testing.T) {
		// cash_redeem requires the invoice amount to exactly equal the
		// recipient's declared slice — no more "comfortably larger" funding
		// headroom needed (or possible): the child is funded with exactly
		// what it will claim.
		const claimAmountMloki = happyPathAmountMloki

		invoice := mintInvoiceFromHub(t, hub, claimAmountMloki)
		cashChild := mintCashChild(t, hub, claimAmountMloki)

		payResult := claimFullSlice(t, cashChild, invoice)
		require.NotEmpty(t, payResult.Preimage)

		var childBalance GetBalanceResult
		require.NoError(t, cashChild.Client.Call(ctxT(t), "get_balance", struct{}{}, &childBalance))
		require.LessOrEqual(t, childBalance.Balance, int64(0), "the child's slice must be fully drained in one shot")

		// A second claim attempt (same identity, same wallet) must be rejected
		// — the atomic claim guard prevents any double-payout, replay or not.
		secondProof := buildClaimProofEvent(t, cashChild.BeneficiaryPrivkey, cashChild.WalletPubkey, invoice.PaymentHash, nil, time.Now())
		var secondResult ClaimFundsResult
		err := cashChild.Client.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice:       invoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: cashChild.BeneficiaryPubkey,
			IdentityEvent: eventJSON(t, secondProof),
		}, &secondResult)
		requireNWCErrorCode(t, err, constants.ERROR_NOT_FOUND)
	})

	t.Run("ClaimFunds_AmountMismatch_Rejected", func(t *testing.T) {
		const cashFundingMloki = 1000
		const invoiceAmountMloki = cashFundingMloki * 100 // far more than the Cash child's declared slice

		invoice := mintInvoiceFromHub(t, hub, invoiceAmountMloki)
		cashChild := mintCashChild(t, hub, cashFundingMloki)

		proof := buildClaimProofEvent(t, cashChild.BeneficiaryPrivkey, cashChild.WalletPubkey, invoice.PaymentHash, nil, time.Now())
		var payResult ClaimFundsResult
		err := cashChild.Client.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice:       invoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: cashChild.BeneficiaryPubkey,
			IdentityEvent: eventJSON(t, proof),
		}, &payResult)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)

		// The slice must remain claimable after a rejected mismatched attempt.
		correctInvoice := mintInvoiceFromHub(t, hub, cashFundingMloki)
		retryResult := claimFullSlice(t, cashChild, correctInvoice)
		require.NotEmpty(t, retryResult.Preimage, "a corrected, matching-amount claim must still succeed after an earlier mismatch")
	})
}
