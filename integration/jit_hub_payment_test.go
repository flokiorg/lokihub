//go:build integration

// jit_hub_payment_test.go exercises real money movement using only a jit_hub's
// own wallet as the invoice source, rather than depending on a circle_hub
// being configured (see cross_test.go for the circle_hub-sourced equivalent).
// A jit_hub granted make_invoice scope in addition to its usual get_balance
// can mint a real invoice for one of its own freshly-minted JIT children to
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
func mintInvoiceFromHub(t *testing.T, hub JITHubConfig, amountMloki uint64) MakeInvoiceResult {
	t.Helper()
	hubClient := mustConnect(t, hub.Connection)

	var invoice MakeInvoiceResult
	err := hubClient.Call(ctxT(t), "make_invoice", MakeInvoiceParams{
		Amount:      amountMloki,
		Description: "integration jit_hub-sourced payment test",
	}, &invoice)
	if err != nil {
		t.Skipf("skipping: jit_hub %q isn't granted make_invoice scope (%v) - see integration/README.md", hub.Name, err)
	}
	require.NotEmpty(t, invoice.Invoice)
	return invoice
}

func TestJITHubPayments(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralJITHub(t, cfg, "jit-hub-payments", nil)
	testJITHubPayments(t, hub)
}

func testJITHubPayments(t *testing.T, hub JITHubConfig) {
	t.Run("ClaimFunds_JITChildClaimsHubMintedInvoice_HappyPath", func(t *testing.T) {
		// claim_funds requires the invoice amount to exactly equal the
		// recipient's declared slice — no more "comfortably larger" funding
		// headroom needed (or possible): the child is funded with exactly
		// what it will claim.
		const claimAmountMloki = happyPathAmountMloki

		invoice := mintInvoiceFromHub(t, hub, claimAmountMloki)
		jitChild := mintJITChild(t, hub, claimAmountMloki)

		payResult := claimFullSlice(t, jitChild, invoice)
		require.NotEmpty(t, payResult.Preimage)

		var childBalance GetBalanceResult
		require.NoError(t, jitChild.Client.Call(ctxT(t), "get_balance", struct{}{}, &childBalance))
		require.LessOrEqual(t, childBalance.Balance, int64(0), "the child's slice must be fully drained in one shot")

		// A second claim attempt (same identity, same wallet) must be rejected
		// — the atomic claim guard prevents any double-payout, replay or not.
		secondProof := buildClaimProofEvent(t, jitChild.BeneficiaryPrivkey, jitChild.WalletPubkey, invoice.PaymentHash, nil, time.Now())
		var secondResult ClaimFundsResult
		err := jitChild.Client.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:       invoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: jitChild.BeneficiaryPubkey,
			IdentityEvent: eventJSON(t, secondProof),
		}, &secondResult)
		requireNWCErrorCode(t, err, constants.ERROR_NOT_FOUND)
	})

	t.Run("ClaimFunds_AmountMismatch_Rejected", func(t *testing.T) {
		const jitFundingMloki = 1000
		const invoiceAmountMloki = jitFundingMloki * 100 // far more than the JIT child's declared slice

		invoice := mintInvoiceFromHub(t, hub, invoiceAmountMloki)
		jitChild := mintJITChild(t, hub, jitFundingMloki)

		proof := buildClaimProofEvent(t, jitChild.BeneficiaryPrivkey, jitChild.WalletPubkey, invoice.PaymentHash, nil, time.Now())
		var payResult ClaimFundsResult
		err := jitChild.Client.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:       invoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: jitChild.BeneficiaryPubkey,
			IdentityEvent: eventJSON(t, proof),
		}, &payResult)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)

		// The slice must remain claimable after a rejected mismatched attempt.
		correctInvoice := mintInvoiceFromHub(t, hub, jitFundingMloki)
		retryResult := claimFullSlice(t, jitChild, correctInvoice)
		require.NotEmpty(t, retryResult.Preimage, "a corrected, matching-amount claim must still succeed after an earlier mismatch")
	})
}
