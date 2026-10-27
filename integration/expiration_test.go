//go:build integration

// expiration_test.go exercises what actually happens once a real wallet's
// own expiry lapses - both for a circle_wallet/cash_wallet child (cash_wallet's
// own claim-time expiry is additionally covered end to end by
// cash_redeem_test.go's WalletExpired_ClaimRejected) and for a hub's own
// parent-level permission, answering two questions the rest of the suite
// doesn't: (1) is expiry enforced uniformly across every method on a child,
// or only some of them, and (2) does a hub's own expiry cascade to wallets
// it already minted, or is each child's validity fully independent once
// created. Both questions are answered identically for cash_hub and
// circle_hub, since both go through the exact same generic
// AppPermission-based expiry check (nip47/permissions/permissions.go) - this
// file exercises both hub types to prove that's actually true, not just
// assumed from reading the code once.
package integration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// shortLivedExpirySecs mirrors cash_redeem_test.go's WalletExpired_ClaimRejected:
// short enough to expire mid-test, long enough that the create/connect setup
// above it can't itself race past it.
const shortLivedExpirySecs = 2

// waitPastExpiry sleeps past a shortLivedExpirySecs wallet's own expiry -
// same margin cash_redeem_test.go uses, well before the 5-minute background
// cleanup ticker (service/cash_cleanup_service.go) would ever sweep it, so
// this is genuinely exercising the live permission check, not racing
// deletion.
func waitPastExpiry() { time.Sleep(3 * time.Second) }

// TestCircleWallet_Expiry_PayoutsAndReadsSurvive mints a real, short-lived
// circle_wallet child and lets it expire. An Expired wallet (NIP-CW §What a
// Circle Wallet Connection Allows) still serves get_info, get_balance,
// list_transactions and payouts, so its member can move the balance out
// during the grace; make_invoice and get_budget are refused RESTRICTED.
func TestCircleWallet_Expiry_PayoutsAndReadsSurvive(t *testing.T) {
	cfg := requireConfig(t)
	privkey := newTestPrivkey(t)
	hub, _, _ := createEphemeralCircleHub(t, cfg, "circle-expiry-hub", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{FundLoki: 10_000})
	hubClient := mustConnect(t, hub.Connection)
	pubkey := mustPubkey(t, privkey)
	identityEvent := buildCircleWalletIdentityEvent(t, privkey, hubClient.WalletPubkey())

	var created CreateCircleWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     circleHappyPathAmountMloki,
		Expiry:        shortLivedExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, identityEvent),
	}, &created))

	circleDetails, err := DecryptCircleWalletDetails(privkey, hubClient.WalletPubkey(), created)
	require.NoError(t, err)
	memberConn := CircleWalletConnection(circleDetails, pubkey)
	child := mustConnectCircleWallet(t, memberConn)

	// Sanity: fully live before expiry, so what we observe afterward is
	// actually caused by expiry and not some unrelated setup mistake.
	var before GetBalanceResult
	require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &before))

	waitPastExpiry()

	t.Run("GetBalance_Expired_Served", func(t *testing.T) {
		var result GetBalanceResult
		require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &result))
	})

	t.Run("MakeInvoice_Expired_Restricted", func(t *testing.T) {
		var result MakeInvoiceResult
		err := child.Call(ctxT(t), "make_invoice", MakeInvoiceParams{Amount: 1000, Description: "integration expired-wallet make_invoice test"}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
	})

	t.Run("PayInvoice_Expired_Served", func(t *testing.T) {
		var result PayInvoiceResult
		// Served: an empty invoice reaches the controller's own validation
		// instead of the state gate.
		err := child.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: ""}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("ListTransactions_Expired_Served", func(t *testing.T) {
		var result ListTransactionsResult
		require.NoError(t, child.Call(ctxT(t), "list_transactions", ListTransactionsParams{Limit: 10}, &result))
	})

	t.Run("GetInfo_Expired_Served", func(t *testing.T) {
		var result GetInfoResult
		require.NoError(t, child.Call(ctxT(t), "get_info", struct{}{}, &result))
	})

	t.Run("GetBudget_Expired_Restricted", func(t *testing.T) {
		var result GetBudgetResult
		requireNWCErrorCode(t, child.Call(ctxT(t), "get_budget", struct{}{}, &result), constants.ERROR_RESTRICTED)
	})
}

// TestCashHub_ParentExpiry_HubRejectedButAlreadyMintedChildKeepsWorking asks
// the "parent" half of the same question: once a cash_hub's OWN permission
// (the "cash_hub" scope letting it call mint_cash) expires, does that
// cascade to wallets it already minted, or is each child independent? Every
// AppPermission row (nip47/permissions/permissions.go's HasPermission) is
// keyed and expiry-checked purely by its own app id - there is no join back
// to a parent app anywhere in that check - so the prediction going in is
// independence: the hub's own mint_cash call should start failing,
// while a child it minted before that point keeps working for the rest of
// its own, separately-tracked expiry.
func TestCashHub_ParentExpiry_HubRejectedButAlreadyMintedChildKeepsWorking(t *testing.T) {
	cfg := requireConfig(t)

	hubExpiresAt := time.Now().Add(shortLivedExpirySecs * time.Second)
	hub, _, _ := createEphemeralCashHub(t, cfg, "cash-parent-expiry-hub", &hubExpiresAt)
	hubClient := mustConnect(t, hub.Connection)

	// Minted BEFORE the hub's own expiry, with its own much-longer expiry -
	// isolates "does the parent's expiry cascade" from "did the child's own
	// expiry also just happen to lapse". createEphemeralCashHub's own
	// t.Cleanup sweeps every child this hub ever mints, so no extra cleanup
	// is needed here even though the claim below fully drains it.
	const childAmountMloki = 5000
	beneficiaryPriv := newTestPrivkey(t)
	beneficiaryPub := mustPubkey(t, beneficiaryPriv)
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(beneficiaryPub, childAmountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))
	child := mustConnectBill(t, created.CashToken, beneficiaryPriv)

	waitPastExpiry()

	t.Run("Hub_MintCash_RejectedOnceHubExpired", func(t *testing.T) {
		var result MintCashResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(mustPubkey(t, newTestPrivkey(t)), childAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_EXPIRED)
	})

	t.Run("ExistingChild_ClaimFunds_StillWorksAfterParentHubExpired", func(t *testing.T) {
		invoice := mintInvoiceFromSimpleWallet(t, cfg, childAmountMloki, "integration parent-expiry test (child survives)")
		proof := buildClaimProofEvent(t, beneficiaryPriv, created.WalletPubkey, invoice.PaymentHash, nil, time.Now())
		var result ClaimFundsResult
		err := child.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice:       invoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: beneficiaryPub,
			IdentityEvent: eventJSON(t, proof),
		}, &result)
		require.NoError(t, err, "a child minted before its hub's own permission expired must keep working - child expiry is tracked independently of the parent's")
		require.NotEmpty(t, result.Preimage)
	})
}

// TestCircleHub_ParentExpiry_HubRejectedButAlreadyMintedChildKeepsWorking is
// TestCashHub_ParentExpiry_...'s circle_hub mirror - both hub kinds share the
// exact same generic AppPermission-based expiry check, so this proves the
// independence finding isn't specific to cash_hub's own request path.
func TestCircleHub_ParentExpiry_HubRejectedButAlreadyMintedChildKeepsWorking(t *testing.T) {
	cfg := requireConfig(t)

	hubExpiresAt := time.Now().Add(shortLivedExpirySecs * time.Second)
	privkey := newTestPrivkey(t)
	hub, _, _ := createEphemeralCircleHub(t, cfg, "circle-parent-expiry-hub", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{FundLoki: 10_000, ExpiresAt: &hubExpiresAt})
	hubClient := mustConnect(t, hub.Connection)
	pubkey := mustPubkey(t, privkey)

	// Minted BEFORE the hub's own expiry, with its own much-longer expiry -
	// isolates "does the parent's expiry cascade" from "did the child's own
	// expiry also just happen to lapse". createEphemeralCircleHub's own
	// t.Cleanup sweeps every child this hub ever mints.
	identityEvent := buildCircleWalletIdentityEvent(t, privkey, hubClient.WalletPubkey())
	var created CreateCircleWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     circleHappyPathAmountMloki,
		Expiry:        happyPathExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, identityEvent),
	}, &created))
	circleDetails, err := DecryptCircleWalletDetails(privkey, hubClient.WalletPubkey(), created)
	require.NoError(t, err)
	memberConn := CircleWalletConnection(circleDetails, pubkey)
	child := mustConnectCircleWallet(t, memberConn)

	waitPastExpiry()

	t.Run("Hub_CreateCircleWallet_RejectedOnceHubExpired", func(t *testing.T) {
		otherPriv := newTestPrivkey(t)
		otherIdentityEvent := buildCircleWalletIdentityEvent(t, otherPriv, hubClient.WalletPubkey())
		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        mustPubkey(t, otherPriv),
			MaxAmount:     circleHappyPathAmountMloki,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: eventJSON(t, otherIdentityEvent),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_EXPIRED)
	})

	t.Run("ExistingChild_GetBalance_StillWorksAfterParentHubExpired", func(t *testing.T) {
		var result GetBalanceResult
		err := child.Call(ctxT(t), "get_balance", struct{}{}, &result)
		require.NoError(t, err, "a child minted before its hub's own permission expired must keep working - child expiry is tracked independently of the parent's")
	})
}
