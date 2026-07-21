//go:build integration

// expiration_test.go exercises what actually happens once a real wallet's
// own expiry lapses - both for a circle_wallet/jit_wallet child (jit_wallet's
// own claim-time expiry is additionally covered end to end by
// claim_funds_test.go's WalletExpired_ClaimRejected) and for a hub's own
// parent-level permission, answering two questions the rest of the suite
// doesn't: (1) is expiry enforced uniformly across every method on a child,
// or only some of them, and (2) does a hub's own expiry cascade to wallets
// it already minted, or is each child's validity fully independent once
// created. Both questions are answered identically for jit_hub and
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
	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// shortLivedExpirySecs mirrors claim_funds_test.go's WalletExpired_ClaimRejected:
// short enough to expire mid-test, long enough that the create/connect setup
// above it can't itself race past it.
const shortLivedExpirySecs = 2

// waitPastExpiry sleeps past a shortLivedExpirySecs wallet's own expiry -
// same margin claim_funds_test.go uses, well before the 5-minute background
// cleanup ticker (service/jit_cleanup_service.go) would ever sweep it, so
// this is genuinely exercising the live permission check, not racing
// deletion.
func waitPastExpiry() { time.Sleep(3 * time.Second) }

// TestCircleWallet_Expiry_MoneyMovingScopesRejectedButBudgetAndInfoSurvive
// mints a real, short-lived circle_wallet child and lets it actually expire,
// then checks every one of its granted methods individually rather than
// assuming expiry is all-or-nothing. It isn't: get_info and get_budget are
// both on permissions.GetAlwaysGrantedMethods() and bypass the generic
// scope-permission gate entirely (nip47/event_handler.go checks that list
// BEFORE ever consulting HasPermission/AppPermission.ExpiresAt) - so they
// keep answering normally forever, while every money-moving method
// (get_balance, make_invoice, pay_invoice, list_transactions) is rejected
// with ERROR_EXPIRED the moment the wallet's own expiry passes. A circle
// wallet child's balance sits inert but visible/reportable, not
// invisible, once expired.
func TestCircleWallet_Expiry_MoneyMovingScopesRejectedButBudgetAndInfoSurvive(t *testing.T) {
	cfg := requireConfig(t)
	privkey := newTestPrivkey(t)
	hub, _, _ := createEphemeralCircleHub(t, cfg, "circle-expiry-hub", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{FundLoki: 10_000})
	hubClient := mustConnect(t, hub.Connection)
	pubkey := mustPubkey(t, privkey)
	identityEvent := buildCircleWalletIdentityEvent(t, privkey, hubClient.ClientPubkey())

	var created CreateCircleWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     happyPathAmountMloki,
		Expiry:        shortLivedExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, identityEvent),
	}, &created))

	pairingURI, err := nwcclient.DecryptPairingURI(privkey, created.WalletPubkey, created.EncryptedPairingURI)
	require.NoError(t, err)
	child := mustConnect(t, pairingURI)

	// Sanity: fully live before expiry, so what we observe afterward is
	// actually caused by expiry and not some unrelated setup mistake.
	var before GetBalanceResult
	require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &before))

	waitPastExpiry()

	t.Run("GetBalance_Expired_Rejected", func(t *testing.T) {
		var result GetBalanceResult
		err := child.Call(ctxT(t), "get_balance", struct{}{}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_EXPIRED)
	})

	t.Run("MakeInvoice_Expired_Rejected", func(t *testing.T) {
		var result MakeInvoiceResult
		err := child.Call(ctxT(t), "make_invoice", MakeInvoiceParams{Amount: 1000, Description: "integration expired-wallet make_invoice test"}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_EXPIRED)
	})

	t.Run("PayInvoice_Expired_Rejected", func(t *testing.T) {
		var result PayInvoiceResult
		// Permission expiry is checked generically in event_handler.go before
		// any request reaches pay_invoice_controller.go, so an empty/invalid
		// invoice string still surfaces ERROR_EXPIRED rather than a param
		// validation error - see get_budget_controller.go's own comment on
		// this same dispatch-order guarantee.
		err := child.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: ""}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_EXPIRED)
	})

	t.Run("ListTransactions_Expired_Rejected", func(t *testing.T) {
		var result ListTransactionsResult
		err := child.Call(ctxT(t), "list_transactions", ListTransactionsParams{Limit: 10}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_EXPIRED)
	})

	t.Run("GetInfo_Expired_StillReachable", func(t *testing.T) {
		var result GetInfoResult
		require.NoError(t, child.Call(ctxT(t), "get_info", struct{}{}, &result),
			"get_info is always-granted (permissions.GetAlwaysGrantedMethods) - it must survive the wallet's own expiry")
	})

	t.Run("GetBudget_Expired_StillReachable", func(t *testing.T) {
		var result GetBudgetResult
		require.NoError(t, child.Call(ctxT(t), "get_budget", struct{}{}, &result),
			"get_budget is always-granted too - an expired circle wallet's budget stays reportable even though it can no longer move money")
		require.EqualValues(t, happyPathAmountMloki, result.TotalBudget)
	})
}

// TestJITHub_ParentExpiry_HubRejectedButAlreadyMintedChildKeepsWorking asks
// the "parent" half of the same question: once a jit_hub's OWN permission
// (the "jit_hub" scope letting it call create_jit_wallet) expires, does that
// cascade to wallets it already minted, or is each child independent? Every
// AppPermission row (nip47/permissions/permissions.go's HasPermission) is
// keyed and expiry-checked purely by its own app id - there is no join back
// to a parent app anywhere in that check - so the prediction going in is
// independence: the hub's own create_jit_wallet call should start failing,
// while a child it minted before that point keeps working for the rest of
// its own, separately-tracked expiry.
func TestJITHub_ParentExpiry_HubRejectedButAlreadyMintedChildKeepsWorking(t *testing.T) {
	cfg := requireConfig(t)

	hubExpiresAt := time.Now().Add(shortLivedExpirySecs * time.Second)
	hub, _, _ := createEphemeralJITHub(t, cfg, "jit-parent-expiry-hub", &hubExpiresAt)
	hubClient := mustConnect(t, hub.Connection)

	// Minted BEFORE the hub's own expiry, with its own much-longer expiry -
	// isolates "does the parent's expiry cascade" from "did the child's own
	// expiry also just happen to lapse". createEphemeralJITHub's own
	// t.Cleanup sweeps every child this hub ever mints, so no extra cleanup
	// is needed here even though the claim below fully drains it.
	const childAmountMloki = 5000
	beneficiaryPriv := newTestPrivkey(t)
	beneficiaryPub := mustPubkey(t, beneficiaryPriv)
	var created CreateJITWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
		Recipients: onePubkeyRecipient(beneficiaryPub, childAmountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))
	child := mustConnect(t, created.PairingURI)

	waitPastExpiry()

	t.Run("Hub_CreateJITWallet_RejectedOnceHubExpired", func(t *testing.T) {
		var result CreateJITWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(mustPubkey(t, newTestPrivkey(t)), childAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_EXPIRED)
	})

	t.Run("ExistingChild_ClaimFunds_StillWorksAfterParentHubExpired", func(t *testing.T) {
		invoice := mintInvoiceFromSimpleWallet(t, cfg, childAmountMloki, "integration parent-expiry test (child survives)")
		proof := buildClaimProofEvent(t, beneficiaryPriv, created.WalletPubkey, invoice.PaymentHash, nil, time.Now())
		var result ClaimFundsResult
		err := child.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
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
// TestJITHub_ParentExpiry_...'s circle_hub mirror - both hub kinds share the
// exact same generic AppPermission-based expiry check, so this proves the
// independence finding isn't specific to jit_hub's own request path.
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
	identityEvent := buildCircleWalletIdentityEvent(t, privkey, hubClient.ClientPubkey())
	var created CreateCircleWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     happyPathAmountMloki,
		Expiry:        happyPathExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, identityEvent),
	}, &created))
	pairingURI, err := nwcclient.DecryptPairingURI(privkey, created.WalletPubkey, created.EncryptedPairingURI)
	require.NoError(t, err)
	child := mustConnect(t, pairingURI)

	waitPastExpiry()

	t.Run("Hub_CreateCircleWallet_RejectedOnceHubExpired", func(t *testing.T) {
		otherPriv := newTestPrivkey(t)
		otherIdentityEvent := buildCircleWalletIdentityEvent(t, otherPriv, hubClient.ClientPubkey())
		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        mustPubkey(t, otherPriv),
			MaxAmount:     happyPathAmountMloki,
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
