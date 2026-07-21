//go:build integration

// delete_test.go treats hub/child deletion as a first-class subject under
// test, rather than the pure fixture-teardown role it plays everywhere else
// in this suite (see admin_client.go's deleteCircleChild/deleteJITWallet/
// deleteApp and ephemeral_test.go's own t.Cleanup usage). It exercises real
// scenarios around apps.DeleteApp's child-count guard and
// service.ReclaimAndDeleteSubWallet's reclaim/settlement-deferral behavior
// for both hub kinds.
package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
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

// TestDeleteCircleHub_RefusedWhileChildrenExist proves apps.DeleteApp's own
// child-count guard: a circle_hub with a live circle_wallet child attached
// must refuse deletion outright (never orphan the child's ParentAppID, which
// has no DB-level cascade), and must succeed once that child is gone.
func TestDeleteCircleHub_RefusedWhileChildrenExist(t *testing.T) {
	cfg := requireConfig(t)
	privkey := newTestPrivkey(t)
	hub, hubAppID, admin := createEphemeralCircleHub(t, cfg, "delete-guard-circle-hub", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{FundLoki: 10_000})
	hubClient := mustConnect(t, hub.Connection)
	pubkey := mustPubkey(t, privkey)
	identityEvent := buildCircleWalletIdentityEvent(t, privkey, hubClient.ClientPubkey())

	var created CreateCircleWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     happyPathAmountMloki,
		Expiry:        happyPathExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, identityEvent),
	}, &created))

	err := admin.deleteApp(hubAppID)
	require.Error(t, err, "a circle_hub with a live child must refuse deletion")
	require.True(t, strings.Contains(err.Error(), "member wallet"), "expected apps.DeleteApp's own child-count guard message, got: %v", err)

	childAppID := findCircleChildAppID(t, admin, hubAppID, pubkey)
	require.NoError(t, admin.deleteCircleChild(hubAppID, childAppID))
	require.NoError(t, admin.deleteApp(hubAppID), "once the only child is gone, the hub itself must now delete cleanly")
}

// TestDeleteJITHub_RefusedWhileChildrenExist is
// TestDeleteCircleHub_RefusedWhileChildrenExist's jit_hub mirror.
func TestDeleteJITHub_RefusedWhileChildrenExist(t *testing.T) {
	cfg := requireConfig(t)
	hub, hubAppID, admin := createEphemeralJITHub(t, cfg, "delete-guard-jit-hub", nil)
	hubClient := mustConnect(t, hub.Connection)

	beneficiaryPub := mustPubkey(t, newTestPrivkey(t))
	var created CreateJITWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
		Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))

	err := admin.deleteApp(hubAppID)
	require.Error(t, err, "a jit_hub with a live child must refuse deletion")
	require.True(t, strings.Contains(err.Error(), "issued wallet"), "expected apps.DeleteApp's own child-count guard message, got: %v", err)

	claims, err := admin.listJITWalletClaims(hubAppID)
	require.NoError(t, err)
	require.NotEmpty(t, claims)
	require.NoError(t, admin.deleteJITWallet(hubAppID, claims[0].WalletAppID))
	require.NoError(t, admin.deleteApp(hubAppID), "once the only child is gone, the hub itself must now delete cleanly")
}

// TestDeleteCircleChild_ReclaimsBalanceToHub proves
// service.ReclaimAndDeleteSubWallet's reclaim half: deleting a funded child
// must credit its exact remaining isolated balance back to the parent hub,
// not write it off.
func TestDeleteCircleChild_ReclaimsBalanceToHub(t *testing.T) {
	cfg := requireConfig(t)
	privkey := newTestPrivkey(t)
	hub, hubAppID, admin := createEphemeralCircleHub(t, cfg, "delete-reclaim-circle-hub", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{FundLoki: 10_000})
	hubClient := mustConnect(t, hub.Connection)
	pubkey := mustPubkey(t, privkey)
	identityEvent := buildCircleWalletIdentityEvent(t, privkey, hubClient.ClientPubkey())

	var hubBalanceBefore GetBalanceResult
	require.NoError(t, hubClient.Call(ctxT(t), "get_balance", struct{}{}, &hubBalanceBefore))

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

	const fundAmountMloki = 3000
	fundCircleChild(t, cfg, child, fundAmountMloki, "integration delete-reclaim fund")

	var childBalance GetBalanceResult
	require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &childBalance))
	require.EqualValues(t, fundAmountMloki, childBalance.Balance)

	childAppID := findCircleChildAppID(t, admin, hubAppID, pubkey)
	require.NoError(t, admin.deleteCircleChild(hubAppID, childAppID))

	var hubBalanceAfter GetBalanceResult
	require.NoError(t, hubClient.Call(ctxT(t), "get_balance", struct{}{}, &hubBalanceAfter))
	require.EqualValues(t, hubBalanceBefore.Balance+fundAmountMloki, hubBalanceAfter.Balance,
		"deleting a funded child must reclaim its exact remaining balance back to the hub")
}

// TestDeleteJITWallet_UnclaimedReclaimsFullAmountToHub is
// TestDeleteCircleChild_ReclaimsBalanceToHub's jit_hub mirror: a jit_wallet
// child is pre-funded with its full declared amount at creation time (moved
// out of the hub's own balance immediately, unlike a circle child which
// starts at zero) - deleting it before anyone claims it must return that
// entire amount to the hub, as if it had never been minted.
func TestDeleteJITWallet_UnclaimedReclaimsFullAmountToHub(t *testing.T) {
	cfg := requireConfig(t)
	hub, hubAppID, admin := createEphemeralJITHub(t, cfg, "delete-reclaim-jit-hub", nil)
	hubClient := mustConnect(t, hub.Connection)

	var hubBalanceBefore GetBalanceResult
	require.NoError(t, hubClient.Call(ctxT(t), "get_balance", struct{}{}, &hubBalanceBefore))

	const amountMloki = 5000
	beneficiaryPub := mustPubkey(t, newTestPrivkey(t))
	var created CreateJITWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
		Recipients: onePubkeyRecipient(beneficiaryPub, amountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))

	var hubBalanceMid GetBalanceResult
	require.NoError(t, hubClient.Call(ctxT(t), "get_balance", struct{}{}, &hubBalanceMid))
	require.EqualValues(t, hubBalanceBefore.Balance-amountMloki, hubBalanceMid.Balance,
		"creating the child must have moved the amount out of the hub's own balance")

	claims, err := admin.listJITWalletClaims(hubAppID)
	require.NoError(t, err)
	require.NoError(t, admin.deleteJITWallet(hubAppID, claims[0].WalletAppID))

	var hubBalanceAfter GetBalanceResult
	require.NoError(t, hubClient.Call(ctxT(t), "get_balance", struct{}{}, &hubBalanceAfter))
	require.EqualValues(t, hubBalanceBefore.Balance, hubBalanceAfter.Balance,
		"deleting an unclaimed jit child must reclaim its entire amount back to the hub, as if it never left")
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

	ev1 := distinctCircleWalletIdentityEvent(t, privkey, hubClient.ClientPubkey(), "first")
	var first CreateCircleWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     happyPathAmountMloki,
		Expiry:        happyPathExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, ev1),
	}, &first))

	ev2 := distinctCircleWalletIdentityEvent(t, privkey, hubClient.ClientPubkey(), "second-before-delete")
	var second CreateCircleWalletResult
	err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     happyPathAmountMloki,
		Expiry:        happyPathExpirySecs,
		IdentityEvent: eventJSON(t, ev2),
	}, &second)
	requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)

	childAppID := findCircleChildAppID(t, admin, hubAppID, pubkey)
	require.NoError(t, admin.deleteCircleChild(hubAppID, childAppID))

	ev3 := distinctCircleWalletIdentityEvent(t, privkey, hubClient.ClientPubkey(), "third-after-delete")
	var third CreateCircleWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     happyPathAmountMloki,
		Expiry:        happyPathExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, ev3),
	}, &third), "the same identity must be free to mint again once its earlier wallet was deleted")
	require.NotEmpty(t, third.WalletPubkey)
}

// TestDeleteCircleChild_DeferredWhilePaymentStillSettling proves
// ReclaimAndDeleteSubWallet's HasPendingIncoming deferral end to end: a
// child with any unpaid incoming invoice must refuse deletion (destroying
// the app now would strand a payment that later settles into nothing to
// credit), and must delete cleanly once that invoice is actually paid.
//
// Uses a one-shot admin.do call rather than the retrying deleteCircleChild
// helper - this invoice is deliberately never going to settle on its own,
// so retrying would just burn ~15s to reach the same error.
func TestDeleteCircleChild_DeferredWhilePaymentStillSettling(t *testing.T) {
	cfg := requireConfig(t)
	privkey := newTestPrivkey(t)
	hub, hubAppID, admin := createEphemeralCircleHub(t, cfg, "delete-settling-hub", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{FundLoki: 10_000})
	hubClient := mustConnect(t, hub.Connection)
	pubkey := mustPubkey(t, privkey)
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

	var invoice MakeInvoiceResult
	require.NoError(t, child.Call(ctxT(t), "make_invoice", MakeInvoiceParams{
		Amount:      1000,
		Description: "integration delete-still-settling test",
	}, &invoice))

	childAppID := findCircleChildAppID(t, admin, hubAppID, pubkey)

	deleteErr := admin.do(http.MethodDelete, deleteCircleChildPath(hubAppID, childAppID), nil)
	require.Error(t, deleteErr, "a child with an unpaid incoming invoice must refuse deletion")
	require.True(t, strings.Contains(deleteErr.Error(), "still settling"), "expected HasPendingIncoming's own deferral message, got: %v", deleteErr)

	payInvoiceFromSimpleWallet(t, cfg, invoice.Invoice)

	require.NoError(t, admin.deleteCircleChild(hubAppID, childAppID), "once the invoice is actually paid, deletion must succeed")
}

// TestDeleteCircleChild_AlreadyDeleted_Errors and
// TestDeleteApp_Nonexistent_Errors both prove deletion isn't silently
// idempotent - a second delete of the same child, or a delete of an id that
// never existed, must surface an error rather than a quiet no-op success
// (which would mask a caller's logic bug retrying a delete it thinks failed).
func TestDeleteCircleChild_AlreadyDeleted_Errors(t *testing.T) {
	cfg := requireConfig(t)
	privkey := newTestPrivkey(t)
	hub, hubAppID, admin := createEphemeralCircleHub(t, cfg, "delete-twice-hub", circlePolicyAllowlist, []string{privkey},
		ephemeralCircleHubOpts{FundLoki: 10_000})
	hubClient := mustConnect(t, hub.Connection)
	pubkey := mustPubkey(t, privkey)
	identityEvent := buildCircleWalletIdentityEvent(t, privkey, hubClient.ClientPubkey())

	var created CreateCircleWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        pubkey,
		MaxAmount:     happyPathAmountMloki,
		Expiry:        happyPathExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, identityEvent),
	}, &created))

	childAppID := findCircleChildAppID(t, admin, hubAppID, pubkey)
	require.NoError(t, admin.deleteCircleChild(hubAppID, childAppID))

	err := admin.deleteCircleChild(hubAppID, childAppID)
	require.Error(t, err, "deleting an already-deleted circle child must error, not silently succeed twice")
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

// findJITClaimByIdentity locates the claim row for identityValue among
// claims - listJITWalletClaims returns every recipient across every live
// wallet under the hub, not scoped to one wallet, so callers filter by
// identity to find the one they minted.
func findJITClaimByIdentity(t *testing.T, claims []adminJITWalletClaim, identityValue string) adminJITWalletClaim {
	t.Helper()
	for _, claim := range claims {
		if claim.IdentityValue == identityValue {
			return claim
		}
	}
	t.Fatalf("no claim found for identity %s", identityValue)
	return adminJITWalletClaim{}
}

// TestJITWalletClaim_ListingReflectsDeletionState is the exact regression
// test for a real production incident: a jit_hub became permanently
// undeletable ("still has 1 issued wallet(s)") while its own "issued
// wallets" listing showed nothing at all. Root cause: DeleteJITWalletClaim
// removed a recipient's slice (sweeping its balance back to the hub
// correctly) without ever checking whether that was the wallet's last
// remaining claim - the now-empty wallet survived as an invisible orphan,
// since ListJITWalletClaims is an inner join starting from the claims table
// (a wallet with zero claims can never appear there, under any filter), yet
// apps.DeleteApp's child-count guard kept counting it forever. This exercises
// the fix end to end through the real admin HTTP API (not just the unit
// test in api/jit_wallet_claims_test.go): the listing and the hub's own
// deletability must stay consistent with each other at every step -
// two-recipients-live, one-removed, and both-removed.
func TestJITWalletClaim_ListingReflectsDeletionState(t *testing.T) {
	cfg := requireConfig(t)
	hub, hubAppID, admin := createEphemeralJITHub(t, cfg, "claim-listing-hub", nil)
	hubClient := mustConnect(t, hub.Connection)

	pub1 := mustPubkey(t, newTestPrivkey(t))
	pub2 := mustPubkey(t, newTestPrivkey(t))
	var created CreateJITWalletResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
		Recipients: []JITWalletRecipientParam{
			{IdentityType: "pubkey", IdentityValue: pub1, AmountMloki: happyPathAmountMloki},
			{IdentityType: "pubkey", IdentityValue: pub2, AmountMloki: happyPathAmountMloki},
		},
		Expiry: happyPathExpirySecs,
	}, &created))

	t.Run("BothRecipientsLive_ListingShowsBoth_HubRefusesDeletion", func(t *testing.T) {
		claims, err := admin.listJITWalletClaims(hubAppID)
		require.NoError(t, err)
		require.Len(t, claims, 2)

		err = admin.deleteApp(hubAppID)
		require.Error(t, err, "the hub must refuse deletion while its wallet is still live")
	})

	claim1 := func() adminJITWalletClaim {
		claims, err := admin.listJITWalletClaims(hubAppID)
		require.NoError(t, err)
		return findJITClaimByIdentity(t, claims, pub1)
	}()
	walletAppID := claim1.WalletAppID

	t.Run("OneRecipientRemoved_ListingShowsOnlyTheOther_HubStillRefuses", func(t *testing.T) {
		require.NoError(t, admin.deleteJITWalletClaim(hubAppID, walletAppID, claim1.ID))

		claims, err := admin.listJITWalletClaims(hubAppID)
		require.NoError(t, err)
		require.Len(t, claims, 1, "the listing must still show the wallet - it still has a live recipient")
		require.Equal(t, pub2, claims[0].IdentityValue)

		err = admin.deleteApp(hubAppID)
		require.Error(t, err, "the hub must still refuse deletion - one recipient is still live")
	})

	t.Run("LastRecipientRemoved_ListingGoesToZero_HubBecomesImmediatelyDeletable", func(t *testing.T) {
		claims, err := admin.listJITWalletClaims(hubAppID)
		require.NoError(t, err)
		claim2 := findJITClaimByIdentity(t, claims, pub2)

		require.NoError(t, admin.deleteJITWalletClaim(hubAppID, walletAppID, claim2.ID))

		claimsAfter, err := admin.listJITWalletClaims(hubAppID)
		require.NoError(t, err)
		require.Empty(t, claimsAfter, "the listing must show nothing once every recipient is gone")

		require.NoError(t, admin.deleteApp(hubAppID),
			"once the wallet's last recipient is removed, the hub must be immediately deletable - proving the "+
				"now-empty wallet was itself reclaimed and deleted, not left behind as an orphan the listing "+
				"can never surface again")
	})
}

// TestDeleteJITWalletClaim_WrongHub_Rejected is the regression test for a
// cross-hub deletion bug found in code review: DELETE
// /api/apps/:id/jit-wallets/:walletId/claims/:claimId used to never validate
// that walletId/claimId actually belonged to the hub named by :id - the
// server-side handler parsed the URL's other IDs but silently ignored the
// hub id entirely. A caller scoped to hub A's endpoint could delete (and
// redirect the sweep-back of) a claim that actually belonged to a completely
// unrelated hub B, simply by supplying hub B's walletId/claimId while hitting
// hub A's URL.
func TestDeleteJITWalletClaim_WrongHub_Rejected(t *testing.T) {
	cfg := requireConfig(t)
	_, hubAppIDA, admin := createEphemeralJITHub(t, cfg, "wrong-hub-a", nil)
	hubB, hubAppIDB, _ := createEphemeralJITHub(t, cfg, "wrong-hub-b", nil)

	hubBClient := mustConnect(t, hubB.Connection)
	pub := mustPubkey(t, newTestPrivkey(t))
	var created CreateJITWalletResult
	require.NoError(t, hubBClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
		Recipients: []JITWalletRecipientParam{
			{IdentityType: "pubkey", IdentityValue: pub, AmountMloki: happyPathAmountMloki},
		},
		Expiry: happyPathExpirySecs,
	}, &created))

	claimsB, err := admin.listJITWalletClaims(hubAppIDB)
	require.NoError(t, err)
	require.Len(t, claimsB, 1)
	claimB := claimsB[0]

	// hubA's own ID in the URL, but walletAppID/claimID actually belong to hubB.
	err = admin.deleteJITWalletClaim(hubAppIDA, claimB.WalletAppID, claimB.ID)
	require.Error(t, err, "a claim belonging to a different hub must not be deletable through this hub's endpoint")

	claimsBAfter, err := admin.listJITWalletClaims(hubAppIDB)
	require.NoError(t, err)
	require.Len(t, claimsBAfter, 1, "hubB's claim must be untouched by the rejected cross-hub request")
}
