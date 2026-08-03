package apps_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// TestIdentityAuthorityManager_Delete_SucceedsEvenWithOutstandingDependentClaims
// is the UX-audit counterpart to the max_transfers finding, generalized to
// the Identity Authority registry: revoking an IA is a single, global,
// irreversible-in-effect action (the registry is instance-wide, not scoped
// per cash_hub — see IdentityAuthorityManager's own doc comment) that
// immediately and permanently cuts off every connection_key-mode recipient
// who depends on it, everywhere on this node.
//
// NIP-CASH §Security Considerations requires this live re-check ("IA
// revocation MUST be checked live at redemption time") for good reason, and
// cash_redeem_controller.go / cash_transfer_controller.go both correctly
// enforce it (constants.ERROR_RESTRICTED, "the Identity Authority for this
// claim has been revoked" — a clear message IF the recipient tries again).
// But once revoked, that connection_key recipient has no recovery path at
// all: they can't cash_redeem (IA check fails), and they can't cash_transfer
// out to a different identity either (the SAME live IA check gates
// cash_transfer's step 7 for a connection_key CURRENT identity). Their slice
// becomes permanently un-actionable by them; only the Hub owner deleting the
// whole wallet reclaims the balance — back to the OWNER, not the recipient.
//
// This test proves the operator-facing side of that risk has zero guardrail:
// IdentityAuthorityManager.Delete succeeds unconditionally, with no count of
// (let alone a listing of) outstanding unclaimed connection_key slices that
// depend on the IA being removed, even though this action is exactly as
// fund-affecting (from the recipient's point of view) as the now-removed
// max_transfers cap ever was. Compare to apps.DeleteApp's own hub-deletion
// guard, which DOES refuse when a cash_hub still has live cash_wallet
// children (see frontend/src/components/connections/DisconnectCashHub.tsx's
// pre-flight count) — no equivalent guard, or even a warning, exists here.
func TestIdentityAuthorityManager_Delete_SucceedsEvenWithOutstandingDependentClaims(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	iaManager := apps.NewIdentityAuthorityManager(svc.DB)
	ia, err := iaManager.Add("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "Trusted IA", nil)
	require.NoError(t, err)

	trusted, err := iaManager.IsTrusted(ia.Pubkey)
	require.NoError(t, err)
	require.True(t, trusted, "sanity check: IA must be trusted before it's ever used by a live claim")

	// A real cash_hub -> cash_wallet -> connection_key claim, exactly the
	// shape a live recipient depends on.
	hub, _, err := svc.AppsService.CreateCashHub(
		"test-hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		db.CashHubConfig{PerWalletMaxMloki: 100_000, MaxExpSecs: 3600},
	)
	require.NoError(t, err)

	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", 1, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{
			IdentityType:  db.CashIdentityConnectionKey,
			IdentityValue: "some-connection-key",
			IAPubkey:      ia.Pubkey,
			AmountMloki:   50_000, // a real, unclaimed, outstanding entitlement
		},
	}))

	// The action under test: revoke the IA. NOTHING here consults the
	// cash_wallet_claims table, counts dependents, or asks for confirmation.
	err = iaManager.Delete(ia.Pubkey)
	require.NoError(t, err, "IdentityAuthorityManager.Delete has no dependent-claim guard: it always succeeds")

	trustedAfter, err := iaManager.IsTrusted(ia.Pubkey)
	require.NoError(t, err)
	assert.False(t, trustedAfter)

	// The claim is untouched and still references the now-revoked IA — its
	// recipient's slice is now permanently unredeemable AND untransferable
	// (both cash_redeem and cash_transfer re-check IsTrusted live), yet
	// nothing about this Delete call surfaced that fact to whoever just
	// clicked "remove" on the Identity Authorities settings page.
	claimAfter, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityConnectionKey, "some-connection-key")
	require.NoError(t, err)
	require.NotNil(t, claimAfter, "the claim still exists, unclaimed, silently orphaned from any trusted IA")
	assert.Equal(t, ia.Pubkey, claimAfter.IAPubkey)
	assert.Nil(t, claimAfter.ClaimedAt)
	assert.Equal(t, int64(50_000), claimAfter.AmountMloki)
}
