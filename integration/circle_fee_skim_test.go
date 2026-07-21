//go:build integration

// circle_fee_skim_test.go exercises a circle_hub's FeesPpm forwarding fee
// against a real, already-running instance. It mints its own ephemeral JIT
// hub + circle hub (the latter with a deliberately nonzero FeesPpm - see
// feeSkimTestFeesPpm) purely as a funding source for the circle side — the
// JIT hub itself isn't the thing under test — because a circle_wallet
// starts unfunded and can only receive real money via a real invoice paid
// by another wallet (see the mintJITChild/claim_funds pattern cross_test.go
// also uses).
package integration

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// feeSkimTestFeesPpm is deliberately nonzero, unlike ephemeralCircleHubOpts'
// own default of 0 - this test specifically wants a real fee rate configured
// to prove self-payment overrides it, rather than merely observing a no-op
// zero rate.
const feeSkimTestFeesPpm = 100

// feeSkimTestPaymentAmountMloki is deliberately larger than
// happyPathAmountMloki (5,000 mloki) purely so any nonzero skim WOULD be
// clearly visible if the exemption below were ever broken, while comfortably
// fitting under the circle_wallet budget cap mintCircleChild mints with
// (happyPathAmountMloki*10 = 50,000 mloki, "never"-renewing).
const feeSkimTestPaymentAmountMloki = happyPathAmountMloki * 4

// TestCircleHub_FeeSkim_SelfPaymentIsNeverSkimmed proves, against a real
// already-running instance, the product decision behind the FeesPpm
// forwarding fee (transactions_service.go's validateCanPay): a payment that
// settles via lokihub's own self-payment shortcut — hit whenever the invoice
// being paid was minted by an app on this same instance, which is the ONLY
// kind of payment this black-box suite can produce (every configured hub
// here shares one underlying LN node, see integration/README.md point 1) —
// must never be skimmed, regardless of the hub's configured fees_ppm. Only a
// payment that genuinely leaves the instance over the real Lightning network
// is skimmable; that positive case isn't reachable from this harness (there
// is no second, external node to pay out to) and is instead covered at the
// unit level by transactions/circle_fee_skim_test.go's
// TestSendPaymentSync_CircleWallet_FeeSkim_HappyPath.
//
// The wallet pays an invoice it minted for itself, so principal nets to zero
// for the payer/payee (same wallet) regardless of whether it's skimmed —
// isolating any nonzero final delta as unambiguous evidence of a skim, which
// this test asserts must be absent.
func TestCircleHub_FeeSkim_SelfPaymentIsNeverSkimmed(t *testing.T) {
	cfg := requireConfig(t)
	jitHub, _, _ := createEphemeralJITHub(t, cfg, "fee-skim-jit-hub", nil)
	circleHub, _, _ := createEphemeralCircleHub(t, cfg, "fee-skim-circle-hub", circlePolicyAllowlist,
		[]string{newTestPrivkey(t)}, ephemeralCircleHubOpts{FeesPpm: feeSkimTestFeesPpm, FundLoki: 100_000})
	hubClient := mustConnect(t, circleHub.Connection)

	var info GetInfoResult
	require.NoError(t, hubClient.Call(ctxT(t), "get_info", struct{}{}, &info))
	require.NotNil(t, info.CircleWallet, "a circle_hub's own get_info must always advertise circle_wallet terms")
	require.Equal(t, feeSkimTestFeesPpm, info.CircleWallet.FeesPpm)

	// Optional: the hub's own get_balance, to also confirm the credit side
	// stays untouched. Not every operator grants get_balance to the hub's own
	// connection (see TestCrossHub_HubBalance_DecreasesWhenChildMinted's
	// identical pattern), so this half is best-effort — the wallet-side
	// assertion below is the primary, always-run proof.
	var hubBalanceBefore *int64
	var hb GetBalanceResult
	if err := hubClient.Call(ctxT(t), "get_balance", struct{}{}, &hb); err == nil {
		v := hb.Balance
		hubBalanceBefore = &v
	}

	circleChild := mintCircleChild(t, circleHub)

	// Fund circleChild via a real JIT-child claim against its own invoice —
	// mirrors TestCrossHub_ClaimFunds_JITChildClaimsAgainstCircleChildInvoice.
	// claim_funds requires the invoice amount to exactly equal the JIT
	// child's declared slice, so both must be feeSkimTestPaymentAmountMloki
	// exactly — there's no skim to additionally fund for since none should
	// ever apply here.
	jitChild := mintJITChild(t, jitHub, feeSkimTestPaymentAmountMloki)
	var fundInvoice MakeInvoiceResult
	require.NoError(t, circleChild.Call(ctxT(t), "make_invoice", MakeInvoiceParams{
		Amount:      feeSkimTestPaymentAmountMloki,
		Description: "integration fee-skim-exemption funding",
	}, &fundInvoice))
	claimFullSlice(t, jitChild, fundInvoice)

	var before GetBalanceResult
	require.NoError(t, circleChild.Call(ctxT(t), "get_balance", struct{}{}, &before))
	require.GreaterOrEqual(t, before.Balance, int64(feeSkimTestPaymentAmountMloki), "circleChild must be funded before it can self-pay")

	var selfInvoice MakeInvoiceResult
	require.NoError(t, circleChild.Call(ctxT(t), "make_invoice", MakeInvoiceParams{
		Amount:      feeSkimTestPaymentAmountMloki,
		Description: "integration fee-skim-exemption self-payment",
	}, &selfInvoice))

	var payResult PayInvoiceResult
	require.NoError(t, circleChild.Call(ctxT(t), "pay_invoice", PayInvoiceParams{
		Invoice: selfInvoice.Invoice,
	}, &payResult))
	require.NotEmpty(t, payResult.Preimage)

	var after GetBalanceResult
	require.NoError(t, circleChild.Call(ctxT(t), "get_balance", struct{}{}, &after))
	require.Equal(t, before.Balance, after.Balance,
		"a self-paid invoice must net to exactly zero (paid and received by the same wallet, no skim) - any delta means same-instance traffic got wrongly skimmed")

	if hubBalanceBefore != nil {
		var hubAfter GetBalanceResult
		require.NoError(t, hubClient.Call(ctxT(t), "get_balance", struct{}{}, &hubAfter))
		require.Equal(t, *hubBalanceBefore, hubAfter.Balance,
			"circle hub's own balance must be unchanged - a self-payment must never credit it a forwarding fee")
	}
}
