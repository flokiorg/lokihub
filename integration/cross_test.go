//go:build integration

// cross_test.go holds scenarios that span both hub types — the core "real
// money movement" tests, since all configured parents share one underlying
// LN node/lokihub instance (see integration/README.md), a circle_wallet
// child (which can make_invoice) can mint a real, fresh invoice for a
// cash_wallet child (spend-only) to pay as a real internal self-payment.
package integration

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// crossHubFixtures provisions a fresh ephemeral Cash hub and (allowlist-policy)
// Circle hub for one cross-hub test - each call is independent, so the two
// hubs and whatever children get minted under them are this test function's
// own, never shared with another top-level Test function.
func crossHubFixtures(t *testing.T) (CashHubConfig, CircleHubConfig) {
	t.Helper()
	cfg := requireConfig(t)
	cashHub, _, _ := createEphemeralCashHub(t, cfg, "cross-cash-hub", nil)
	circleHub, _, _ := createEphemeralCircleHub(t, cfg, "cross-circle-hub", circlePolicyAllowlist,
		[]string{newTestPrivkey(t)}, ephemeralCircleHubOpts{FundLoki: 100_000})
	return cashHub, circleHub
}

// mintCircleChild mints a fresh circle_wallet child for hub's first
// authorized member.
func mintCircleChild(t *testing.T, hub CircleHubConfig) *nwcclient.Client {
	t.Helper()
	require.NotEmpty(t, hub.Members.AuthorizedPrivkeys, "circle hub %s needs at least one authorized privkey", hub.Name)

	authorizedPrivkey := hub.Members.AuthorizedPrivkeys[0]
	hubClient := mustConnect(t, hub.Connection)
	authorizedPub := mustPubkey(t, authorizedPrivkey)

	identityEvent := buildCircleWalletIdentityEvent(t, authorizedPrivkey, hubClient.ClientPubkey())

	var result CreateCircleWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        authorizedPub,
		MaxAmount:     happyPathAmountMloki * 10,
		Expiry:        happyPathExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, identityEvent),
	}, &result))

	pairingURI, err := nwcclient.DecryptPairingURI(authorizedPrivkey, result.WalletPubkey, result.EncryptedPairingURI)
	require.NoError(t, err)
	return mustConnect(t, pairingURI)
}

// cashChildFixture is a freshly-minted, single-recipient shared Cash wallet:
// Client is already connected via the wallet's one shared PairingURI, and
// BeneficiaryPrivkey/Pubkey are the recipient's own identity, needed
// separately to sign a cash_redeem proof (distinct from the connection's own
// bearer credential — see nip47/controllers/cash_redeem_controller.go).
type cashChildFixture struct {
	Client             *nwcclient.Client
	WalletPubkey       string
	BeneficiaryPrivkey string
	BeneficiaryPubkey  string
	AmountMillis       uint64
}

// mintCashChild creates a fresh cash_wallet child pre-funded with EXACTLY
// amountMloki for one pubkey-mode recipient, and connects to it.
func mintCashChild(t *testing.T, hub CashHubConfig, amountMloki uint64) cashChildFixture {
	t.Helper()
	hubClient := mustConnect(t, hub.Connection)

	beneficiaryPriv := newTestPrivkey(t)
	beneficiaryPub, err := nostr.GetPublicKey(beneficiaryPriv)
	require.NoError(t, err)

	var result MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(beneficiaryPub, amountMloki),
		Expiry:     happyPathExpirySecs,
	}, &result))

	return cashChildFixture{
		Client:             mustConnect(t, result.PairingURI),
		WalletPubkey:       result.WalletPubkey,
		BeneficiaryPrivkey: beneficiaryPriv,
		BeneficiaryPubkey:  beneficiaryPub,
		AmountMillis:       amountMloki,
	}
}

// claimFullSlice signs a claim proof for fixture bound to invoice and calls
// cash_redeem — the recipient's one-shot, full-slice payout. The invoice's
// own amount must exactly equal fixture.AmountMillis (cash_redeem' own
// "not partially, in one shot" rule), so callers must mint invoice for
// exactly that amount.
func claimFullSlice(t *testing.T, fixture cashChildFixture, invoice MakeInvoiceResult) ClaimFundsResult {
	t.Helper()
	proof := buildClaimProofEvent(t, fixture.BeneficiaryPrivkey, fixture.WalletPubkey, invoice.PaymentHash, nil, time.Now())

	var result ClaimFundsResult
	require.NoError(t, fixture.Client.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       invoice.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: fixture.BeneficiaryPubkey,
		IdentityEvent: eventJSON(t, proof),
	}, &result))
	return result
}

func TestCrossHub_ClaimFunds_CashChildClaimsAgainstCircleChildInvoice(t *testing.T) {
	cashHub, circleHub := crossHubFixtures(t)

	circleChild := mintCircleChild(t, circleHub)
	// cash_redeem requires the invoice amount to exactly equal the recipient's
	// declared slice — fund the Cash child with exactly what it will claim,
	// not "comfortably larger" (no more partial-spend concept to leave
	// headroom for).
	const claimAmountMloki = 1000

	// circleChild is a fresh, ephemeral wallet (crossHubFixtures mints a new
	// hub+identity per test), so this delta should just be claimAmountMloki
	// off zero - asserting the delta rather than an absolute keeps this
	// robust even if that assumption ever changes.
	var circleBalanceBefore GetBalanceResult
	require.NoError(t, circleChild.Call(ctxT(t), "get_balance", struct{}{}, &circleBalanceBefore))

	cashChild := mintCashChild(t, cashHub, claimAmountMloki)

	var invoice MakeInvoiceResult
	require.NoError(t, circleChild.Call(ctxT(t), "make_invoice", MakeInvoiceParams{
		Amount:      claimAmountMloki,
		Description: "integration cross-hub cash_redeem test",
	}, &invoice))
	require.NotEmpty(t, invoice.Invoice)

	payResult := claimFullSlice(t, cashChild, invoice)
	require.NotEmpty(t, payResult.Preimage)

	var cashBalance GetBalanceResult
	require.NoError(t, cashChild.Client.Call(ctxT(t), "get_balance", struct{}{}, &cashBalance))
	require.LessOrEqual(t, cashBalance.Balance, int64(0), "the Cash child's slice must be fully drained in one shot")

	var circleBalance GetBalanceResult
	require.NoError(t, circleChild.Call(ctxT(t), "get_balance", struct{}{}, &circleBalance))
	require.EqualValues(t, circleBalanceBefore.Balance+int64(claimAmountMloki), circleBalance.Balance,
		"circle child's balance should increase by exactly the invoiced amount")

	// cash_wallet no longer carries list_transactions (would leak other
	// recipients' payout history on a shared connection) — only the circle
	// side's history can be checked here.
	var circleTxs ListTransactionsResult
	require.NoError(t, circleChild.Call(ctxT(t), "list_transactions", ListTransactionsParams{Limit: 10}, &circleTxs))
	require.True(t, containsPaymentHash(circleTxs.Transactions, invoice.PaymentHash), "circle child's transaction history should show the incoming payment")
}

func TestCrossHub_ClaimFunds_AmountMismatch_Rejected(t *testing.T) {
	cashHub, _ := crossHubFixtures(t)

	const cashFundingMloki = 1000
	// Deliberately doesn't match the Cash child's declared slice. The
	// mismatch check is purely "invoice amount vs. the recipient's declared
	// slice" - it doesn't care who minted the invoice - so this is sourced
	// from a fresh ephemeral simple wallet rather than a circle child. This
	// invoice is intentionally never paid (that's the whole point of the
	// test); minting it on a circle child would leave a permanently-PENDING
	// incoming transaction behind that blocks that child's own eventual
	// cleanup (HasPendingIncoming) - see git history for the runs this once
	// broke, back when hubs were long-lived and shared across runs.
	const invoiceAmountMloki = cashFundingMloki * 2

	cashChild := mintCashChild(t, cashHub, cashFundingMloki)

	invoice := mintInvoiceFromSimpleWallet(t, requireConfig(t), invoiceAmountMloki, "integration cross-hub amount-mismatch test")

	proof := buildClaimProofEvent(t, cashChild.BeneficiaryPrivkey, cashChild.WalletPubkey, invoice.PaymentHash, nil, time.Now())
	var payResult ClaimFundsResult
	err := cashChild.Client.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       invoice.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: cashChild.BeneficiaryPubkey,
		IdentityEvent: eventJSON(t, proof),
	}, &payResult)
	requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
}

func TestCrossHub_HubBalance_DecreasesWhenChildMinted(t *testing.T) {
	cashHub, _ := crossHubFixtures(t)
	hubClient := mustConnect(t, cashHub.Connection)

	var before GetBalanceResult
	if err := hubClient.Call(ctxT(t), "get_balance", struct{}{}, &before); err != nil {
		t.Skipf("skipping: this cash_hub connection isn't granted get_balance scope (%v)", err)
	}

	_ = mintCashChild(t, cashHub, happyPathAmountMloki)

	var after GetBalanceResult
	require.NoError(t, hubClient.Call(ctxT(t), "get_balance", struct{}{}, &after))
	require.Equal(t, before.Balance-int64(happyPathAmountMloki), after.Balance,
		"hub's own isolated balance should decrease by exactly the amount transferred into the new child")
}

func TestCrossHub_GetInfo_AdvertisesExpectedMethods(t *testing.T) {
	cashHub, circleHub := crossHubFixtures(t)

	t.Run("cash_hub", func(t *testing.T) {
		client := mustConnect(t, cashHub.Connection)
		var info GetInfoResult
		require.NoError(t, client.Call(ctxT(t), "get_info", struct{}{}, &info))
		require.Contains(t, info.Methods, constants.NIP47MethodMintCash)
	})

	t.Run("circle_hub", func(t *testing.T) {
		client := mustConnect(t, circleHub.Connection)
		var info GetInfoResult
		require.NoError(t, client.Call(ctxT(t), "get_info", struct{}{}, &info))
		require.Contains(t, info.Methods, constants.NIP47MethodCreateCircleWallet)
	})
}

func containsPaymentHash(txs []MakeInvoiceResult, paymentHash string) bool {
	for _, tx := range txs {
		if tx.PaymentHash == paymentHash {
			return true
		}
	}
	return false
}
