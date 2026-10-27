//go:build integration

// delete_test.go treats hub/child deletion as a first-class subject under
// test, rather than the pure fixture-teardown role it plays everywhere else
// in this suite (see admin_client.go's deleteCircleChild/deleteCashWallet/
// deleteApp and ephemeral_test.go's own t.Cleanup usage). It exercises real
// scenarios around apps.DeleteApp's child-count guard and
// service.ReclaimAndDeleteSubWallet's reclaim/settlement-deferral behavior
// for both hub kinds.
package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// findCircleChildAppID resolves requesterPubkey's circle_wallet child app id
// under hubAppID via the admin API - listCircleChildren returns exactly
// this pairing, but callers otherwise only get the wallet's own NWC pubkey
// back from create_circle_wallet, not its admin app id.
func findCircleChildAppID(t *testing.T, admin *adminClient, hubAppID uint, requesterPubkey string) uint {
	t.Helper()
	children, err := admin.listCircleChildren(hubAppID)
	require.NoError(t, err)
	for _, child := range children {
		if child.RequesterPubkey == requesterPubkey {
			return child.AppID
		}
	}
	t.Fatalf("no circle child found for requester pubkey %s under hub app_id=%d", requesterPubkey, hubAppID)
	return 0
}

// TestDeleteCircleHub_TeardownKeepsTheHubUntilItsLastWallet: deleting a
// circle_hub tears it down (NIP-CW §Lifecycle and Deletion): its wallet moves
// to Closing, the hub stays while it has one, and once the member deletes its
// own (empty) wallet the lifecycle job deletes the hub.
func TestDeleteCircleHub_TeardownKeepsTheHubUntilItsLastWallet(t *testing.T) {
	cfg := requireConfig(t)
	privkey := newTestPrivkey(t)
	hub, hubAppID, admin := createEphemeralCircleHub(t, cfg, "delete-guard-circle-hub", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{FundLoki: 10_000})
	child := mintCircleChildFor(t, hub, privkey)

	require.NoError(t, admin.deleteApp(hubAppID), "a teardown, not a refusal")
	var app map[string]interface{}
	require.NoError(t, admin.do(http.MethodGet, fmt.Sprintf("/api/apps/%d", hubAppID), &app), "the hub stays while it has a wallet")

	var deleted map[string]interface{}
	require.NoError(t, child.Call(ctxT(t), constants.NIP47MethodDeleteCircleWallet, struct{}{}, &deleted),
		"a Closing wallet holding no more than dust is deleted by its member")
	require.Eventually(t, func() bool {
		return admin.do(http.MethodGet, fmt.Sprintf("/api/apps/%d", hubAppID), &app) != nil
	}, 3*time.Minute, 2*time.Second, "the lifecycle job deletes the hub after its last wallet")
}

// TestDeleteCashHub_RefusedWhileChildrenExist: a cash_hub with a live child
// refuses deletion, and once the child is gone it still stays while the mint's
// request record is retained (Q23), which the delete check reports with its
// time.
func TestDeleteCashHub_RefusedWhileChildrenExist(t *testing.T) {
	cfg := requireConfig(t)
	hub, hubAppID, admin := createEphemeralCashHub(t, cfg, "delete-guard-cash-hub", nil)
	hubClient := mustConnect(t, hub.Connection)

	beneficiaryPub := mustPubkey(t, newTestPrivkey(t))
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))

	err := admin.deleteApp(hubAppID)
	require.Error(t, err, "a cash_hub with a live child must refuse deletion")
	require.True(t, strings.Contains(err.Error(), "issued wallet"), "expected apps.DeleteApp's own child-count guard message, got: %v", err)

	claims, err := admin.listCashWalletClaims(hubAppID)
	require.NoError(t, err)
	require.NotEmpty(t, claims)
	require.NoError(t, admin.deleteCashWallet(hubAppID, claims[0].WalletAppID))

	err = admin.deleteApp(hubAppID)
	require.Error(t, err, "the mint's request record keeps the hub")
	require.Contains(t, err.Error(), "request record")
	var check struct {
		Blockers []struct {
			Kind  string `json:"kind"`
			Count int64  `json:"count"`
		} `json:"blockers"`
	}
	require.NoError(t, admin.do(http.MethodGet, fmt.Sprintf("/api/cash-hubs/%d/delete-check", hubAppID), &check))
	require.Len(t, check.Blockers, 1)
	assert.Equal(t, "records", check.Blockers[0].Kind)
}

// TestDeleteCircleChild_ClosesWithoutSweeping: the host's delete of a funded
// member wallet moves it to Closing; its balance stays the member's and nothing
// reaches the hub (NIP-CW §Lifecycle and Deletion: balances are never swept).
func TestDeleteCircleChild_ClosesWithoutSweeping(t *testing.T) {
	cfg := requireConfig(t)
	privkey := newTestPrivkey(t)
	hub, hubAppID, admin := createEphemeralCircleHub(t, cfg, "delete-reclaim-circle-hub", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{FundLoki: 10_000})
	hubClient := mustConnect(t, hub.Connection)
	pubkey := mustPubkey(t, privkey)

	var hubBalanceBefore GetBalanceResult
	require.NoError(t, hubClient.Call(ctxT(t), "get_balance", struct{}{}, &hubBalanceBefore))
	child := mintCircleChildFor(t, hub, privkey)
	const fundAmountMloki = 3000
	fundCircleChild(t, cfg, child, fundAmountMloki, "integration delete-close fund")

	childAppID := findCircleChildAppID(t, admin, hubAppID, pubkey)
	require.NoError(t, admin.deleteCircleChild(hubAppID, childAppID))

	assert.Equal(t, "closing", stateOf(t, admin, hubAppID, pubkey))
	assert.EqualValues(t, fundAmountMloki, balanceOf(t, child), "the balance stays the member's")
	var hubBalanceAfter GetBalanceResult
	require.NoError(t, hubClient.Call(ctxT(t), "get_balance", struct{}{}, &hubBalanceAfter))
	assert.Equal(t, hubBalanceBefore.Balance, hubBalanceAfter.Balance, "nothing reaches the hub")
}

// TestDeleteCashWallet_UnclaimedReclaimsFullAmountToHub is
// TestDeleteCircleChild_ReclaimsBalanceToHub's cash_hub mirror: a cash_wallet
// child is pre-funded with its full declared amount at creation time (moved
// out of the hub's own balance immediately, unlike a circle child which
// starts at zero) - deleting it before anyone claims it must return that
// entire amount to the hub, as if it had never been minted.
func TestDeleteCashWallet_UnclaimedReclaimsFullAmountToHub(t *testing.T) {
	cfg := requireConfig(t)
	hub, hubAppID, admin := createEphemeralCashHub(t, cfg, "delete-reclaim-cash-hub", nil)
	hubClient := mustConnect(t, hub.Connection)

	var hubBalanceBefore GetBalanceResult
	require.NoError(t, hubClient.Call(ctxT(t), "get_balance", struct{}{}, &hubBalanceBefore))

	const amountMloki = 5000
	beneficiaryPub := mustPubkey(t, newTestPrivkey(t))
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(beneficiaryPub, amountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))

	var hubBalanceMid GetBalanceResult
	require.NoError(t, hubClient.Call(ctxT(t), "get_balance", struct{}{}, &hubBalanceMid))
	require.EqualValues(t, hubBalanceBefore.Balance-amountMloki, hubBalanceMid.Balance,
		"creating the child must have moved the amount out of the hub's own balance")

	claims, err := admin.listCashWalletClaims(hubAppID)
	require.NoError(t, err)
	require.NoError(t, admin.deleteCashWallet(hubAppID, claims[0].WalletAppID))

	var hubBalanceAfter GetBalanceResult
	require.NoError(t, hubClient.Call(ctxT(t), "get_balance", struct{}{}, &hubBalanceAfter))
	require.EqualValues(t, hubBalanceBefore.Balance, hubBalanceAfter.Balance,
		"deleting an unclaimed cash child must reclaim its entire amount back to the hub, as if it never left")
}

// TestDeleteCircleChild_FreesIdentityForNewWallet proves the
// one-active-wallet-per-identity cap (create_circle_wallet_controller.go's
// CircleWalletMembership row) is actually released by a delete, not just
// the app row removed: a second request for the same identity is rejected
// while the first wallet is alive, but a third request for that same
// identity succeeds once the first has been deleted.
func TestDeleteCircleChild_FreesIdentityForNewWallet(t *testing.T) {
	cfg := requireConfig(t)
	privkey := newTestPrivkey(t)
	hub, hubAppID, admin := createEphemeralCircleHub(t, cfg, "delete-free-identity-hub", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{FundLoki: 10_000})
	hubClient := mustConnect(t, hub.Connection)
	pubkey := mustPubkey(t, privkey)

	ev1 := distinctCircleWalletIdentityEvent(t, privkey, hubClient.WalletPubkey(), "first")
	var first CreateCircleWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     circleHappyPathAmountMloki,
		Expiry:        happyPathExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, ev1),
	}, &first))

	ev2 := distinctCircleWalletIdentityEvent(t, privkey, hubClient.WalletPubkey(), "second-before-delete")
	var second CreateCircleWalletResult
	err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     circleHappyPathAmountMloki,
		Expiry:        happyPathExpirySecs,
		IdentityEvent: eventJSON(t, ev2),
	}, &second)
	requireNWCErrorCode(t, err, nipcw.ErrWalletExists)

	childAppID := findCircleChildAppID(t, admin, hubAppID, pubkey)
	require.NoError(t, admin.deleteCircleChild(hubAppID, childAppID))

	// The Closing wallet keeps its connection key, so the new join uses a new
	// one (NIP-CW step 9: a key in use is RESTRICTED).
	newConnection := mustPubkey(t, newTestPrivkey(t))
	ev3 := &nostr.Event{Kind: nostrKindCircleIdentityProof, CreatedAt: nostr.Now(),
		Tags: nostr.Tags{{"d", hubClient.WalletPubkey()}, {"connection", newConnection}}}
	require.NoError(t, ev3.Sign(privkey))
	circleProofSigners.Store(ev3.ID, privkey)
	var third CreateCircleWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:           pubkey,
		ConnectionPubkey: newConnection,
		MaxAmount:        circleHappyPathAmountMloki,
		Expiry:           happyPathExpirySecs,
		BudgetRenewal:    constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent:    eventJSON(t, ev3),
	}, &third), "the same identity is free to join again once its earlier wallet is Closing")
	thirdDetails, err := DecryptCircleWalletDetails(privkey, hubClient.WalletPubkey(), third)
	require.NoError(t, err)
	require.NotEmpty(t, thirdDetails.WalletPubkey)
}

// TestDeleteCircleChild_CancelsItsLiveInvoice: closing a member wallet with an
// unpaid invoice cancels the invoice, so a later payment of it fails rather than
// crediting a wallet that can no longer receive.
func TestDeleteCircleChild_CancelsItsLiveInvoice(t *testing.T) {
	cfg := requireConfig(t)
	privkey := newTestPrivkey(t)
	hub, hubAppID, admin := createEphemeralCircleHub(t, cfg, "delete-settling-hub", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{FundLoki: 10_000})
	child := mintCircleChildFor(t, hub, privkey)

	var invoice MakeInvoiceResult
	require.NoError(t, child.Call(ctxT(t), "make_invoice", MakeInvoiceParams{
		Amount:      1000,
		Description: "integration delete-still-settling test",
	}, &invoice))

	childAppID := findCircleChildAppID(t, admin, hubAppID, mustPubkey(t, privkey))
	require.NoError(t, admin.deleteCircleChild(hubAppID, childAppID))

	payer := mustConnect(t, createEphemeralSimpleWallet(t, cfg).Connection)
	var paid PayInvoiceResult
	assert.Error(t, payer.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: invoice.Invoice}, &paid))
	assert.Zero(t, balanceOf(t, child))
}

// TestDeleteCircleChild_Twice_NoChange: a second host delete of a Closing
// wallet changes nothing (its grace is not restarted); a deleted wallet is gone.
func TestDeleteCircleChild_Twice_NoChange(t *testing.T) {
	cfg := requireConfig(t)
	privkey := newTestPrivkey(t)
	hub, hubAppID, admin := createEphemeralCircleHub(t, cfg, "delete-twice-hub", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{FundLoki: 10_000})
	child := mintCircleChildFor(t, hub, privkey)

	childAppID := findCircleChildAppID(t, admin, hubAppID, mustPubkey(t, privkey))
	require.NoError(t, admin.deleteCircleChild(hubAppID, childAppID))
	require.NoError(t, admin.deleteCircleChild(hubAppID, childAppID), "a Closing wallet stays Closing")
	assert.Equal(t, "closing", stateOf(t, admin, hubAppID, mustPubkey(t, privkey)))

	var deleted map[string]interface{}
	require.NoError(t, child.Call(ctxT(t), constants.NIP47MethodDeleteCircleWallet, struct{}{}, &deleted))
	require.Error(t, admin.deleteCircleChild(hubAppID, childAppID), "a deleted wallet is gone")
}

func TestDeleteApp_Nonexistent_Errors(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}
	err := admin.deleteApp(999_999_999)
	require.Error(t, err, "deleting a nonexistent app id must error, not silently succeed")
}

// findCashClaimByIdentity locates the claim row for identityValue among
// claims - listCashWalletClaims returns every recipient across every live
// wallet under the hub, not scoped to one wallet, so callers filter by
// identity to find the one they minted.
func findCashClaimByIdentity(t *testing.T, claims []adminCashWalletClaim, identityValue string) adminCashWalletClaim {
	t.Helper()
	for _, claim := range claims {
		if claim.IdentityValue == identityValue {
			return claim
		}
	}
	t.Fatalf("no claim found for identity %s", identityValue)
	return adminCashWalletClaim{}
}

// TestCashWalletClaim_ListingReflectsDeletionState is the exact regression
// test for a real production incident: a cash_hub became permanently
// undeletable ("still has 1 issued wallet(s)") while its own "issued
// wallets" listing showed nothing at all. Root cause: DeleteCashClaim
// removed a recipient's slice (sweeping its balance back to the hub
// correctly) without ever checking whether that was the wallet's last
// remaining claim - the now-empty wallet survived as an invisible orphan,
// since ListCashWalletClaims is an inner join starting from the claims table
// (a wallet with zero claims can never appear there, under any filter), yet
// apps.DeleteApp's child-count guard kept counting it forever. This exercises
// the fix end to end through the real admin HTTP API (not just the unit
// test in api/cash_wallet_claims_test.go): the listing and the hub's own
// deletability must stay consistent with each other at every step -
// two-recipients-live, one-removed, and both-removed.
func TestCashWalletClaim_ListingReflectsDeletionState(t *testing.T) {
	cfg := requireConfig(t)
	hub, hubAppID, admin := createEphemeralCashHub(t, cfg, "claim-listing-hub", nil)
	hubClient := mustConnect(t, hub.Connection)

	pub1 := mustPubkey(t, newTestPrivkey(t))
	pub2 := mustPubkey(t, newTestPrivkey(t))
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: []CashWalletRecipientParam{
			{IdentityType: "pubkey", IdentityValue: pub1, AmountMillis: happyPathAmountMloki},
			{IdentityType: "pubkey", IdentityValue: pub2, AmountMillis: happyPathAmountMloki},
		},
		Expiry: happyPathExpirySecs,
	}, &created))

	t.Run("BothRecipientsLive_ListingShowsBoth_HubRefusesDeletion", func(t *testing.T) {
		claims, err := admin.listCashWalletClaims(hubAppID)
		require.NoError(t, err)
		require.Len(t, claims, 2)

		err = admin.deleteApp(hubAppID)
		require.Error(t, err, "the hub must refuse deletion while its wallet is still live")
	})

	claim1 := func() adminCashWalletClaim {
		claims, err := admin.listCashWalletClaims(hubAppID)
		require.NoError(t, err)
		return findCashClaimByIdentity(t, claims, pub1)
	}()
	walletAppID := claim1.WalletAppID

	t.Run("OneRecipientRemoved_ListingShowsOnlyTheOther_HubStillRefuses", func(t *testing.T) {
		require.NoError(t, admin.deleteCashWalletClaim(hubAppID, walletAppID, claim1.ID))

		claims, err := admin.listCashWalletClaims(hubAppID)
		require.NoError(t, err)
		live := liveCashClaims(claims)
		require.Len(t, live, 1, "the listing must still show the wallet - it still has a live recipient")
		require.Equal(t, pub2, live[0].IdentityValue)

		// The removed recipient is not simply gone: removing them archives the
		// slice, so the operator keeps a record that they were once owed it.
		archived := archivedCashClaims(claims)
		require.Len(t, archived, 1, "a removed recipient must leave an archived slice behind")
		require.Equal(t, pub1, archived[0].IdentityValue)

		err = admin.deleteApp(hubAppID)
		require.Error(t, err, "the hub must still refuse deletion - one recipient is still live")
	})

	t.Run("LastRecipientRemoved_ListingGoesToZero_NoLiveBillKeepsTheHub", func(t *testing.T) {
		claims, err := admin.listCashWalletClaims(hubAppID)
		require.NoError(t, err)
		claim2 := findCashClaimByIdentity(t, claims, pub2)

		require.NoError(t, admin.deleteCashWalletClaim(hubAppID, walletAppID, claim2.ID))

		claimsAfter, err := admin.listCashWalletClaims(hubAppID)
		require.NoError(t, err)
		require.Empty(t, liveCashClaims(claimsAfter),
			"no live recipient may remain once every one has been removed")
		require.Len(t, archivedCashClaims(claimsAfter), 2,
			"both removed recipients stay in the archive - the wallet is gone, its history is not")

		// The now-empty wallet was itself deleted (no live bill blocks the hub);
		// only the mint's retained request record keeps the hub (Q23).
		err = admin.deleteApp(hubAppID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "request record", "only the retained record, no live bill, keeps the hub")
		require.NotContains(t, err.Error(), "issued wallet")
	})
}

// TestDeleteCashClaim_WrongHub_Rejected is the regression test for a
// cross-hub deletion bug found in code review: DELETE
// /api/apps/:id/cash-wallets/:walletId/claims/:claimId used to never validate
// that walletId/claimId actually belonged to the hub named by :id - the
// server-side handler parsed the URL's other IDs but silently ignored the
// hub id entirely. A caller scoped to hub A's endpoint could delete (and
// redirect the sweep-back of) a claim that actually belonged to a completely
// unrelated hub B, simply by supplying hub B's walletId/claimId while hitting
// hub A's URL.
func TestDeleteCashClaim_WrongHub_Rejected(t *testing.T) {
	cfg := requireConfig(t)
	_, hubAppIDA, admin := createEphemeralCashHub(t, cfg, "wrong-hub-a", nil)
	hubB, hubAppIDB, _ := createEphemeralCashHub(t, cfg, "wrong-hub-b", nil)

	hubBClient := mustConnect(t, hubB.Connection)
	pub := mustPubkey(t, newTestPrivkey(t))
	var created MintCashResult
	require.NoError(t, hubBClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: []CashWalletRecipientParam{
			{IdentityType: "pubkey", IdentityValue: pub, AmountMillis: happyPathAmountMloki},
		},
		Expiry: happyPathExpirySecs,
	}, &created))

	claimsB, err := admin.listCashWalletClaims(hubAppIDB)
	require.NoError(t, err)
	require.Len(t, claimsB, 1)
	claimB := claimsB[0]

	// hubA's own ID in the URL, but walletAppID/claimID actually belong to hubB.
	err = admin.deleteCashWalletClaim(hubAppIDA, claimB.WalletAppID, claimB.ID)
	require.Error(t, err, "a claim belonging to a different hub must not be deletable through this hub's endpoint")

	claimsBAfter, err := admin.listCashWalletClaims(hubAppIDB)
	require.NoError(t, err)
	require.Len(t, claimsBAfter, 1, "hubB's claim must be untouched by the rejected cross-hub request")
}
