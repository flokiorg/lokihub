package api

// Covers DeleteCircleWalletChild: unlike DeleteCircleHub (which only ever
// operates on the whole hub at once), this removes a single circle_wallet
// child in any state — reclaiming any remaining balance back to the hub
// before deleting the child app, the same way DeleteJITHubAllocation handles
// a claimed JIT wallet (see service.ReclaimAndDeleteSubWallet).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
)

func newCircleHub(t *testing.T, svc *tests.TestService, perWalletMaxMloki, maxExpSecs int) *db.App {
	t.Helper()
	hub, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: maxExpSecs, PerWalletMaxMloki: perWalletMaxMloki},
	)
	require.NoError(t, err)
	return hub
}

func TestDeleteCircleWalletChild_Empty_DeletesChild(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 100_000, 3600)
	child := createCircleChild(t, svc, hub, "member")

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteCircleWalletChild(hub.ID, child.ID)
	require.NoError(t, err)

	var count int64
	svc.DB.Model(&db.App{}).Where("id = ?", child.ID).Count(&count)
	assert.Zero(t, count, "the empty circle wallet child must be deleted")
}

func TestDeleteCircleWalletChild_WithBalance_ReclaimsToHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Recognised as a self-payment so the reclaim transfer's incoming leg on
	// the hub settles synchronously.
	svc.LNClient.(*tests.MockLn).Pubkey = selfPaymentPubkey

	hub := newCircleHub(t, svc, 300_000, 3600)
	// Needs pay_invoice (unlike createCircleChild's default GET_BALANCE_SCOPE-only
	// helper) since reclaiming pays out of this child back to the hub.
	child, _, err := svc.AppsService.CreateApp(
		"member", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &hub.ID, db.ParentKindCircle, nil,
	)
	require.NoError(t, err)

	// MockInvoice (used for the reclaim transfer) encodes a fixed 123_000 mloki
	// amount that the mock LN client validates against regardless of the
	// requested amount — fund well above that so the reclaim payment validates.
	const fundedMloki = 200_000
	tests.FundApp(svc, child.ID, fundedMloki, "fundtxhash")

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteCircleWalletChild(hub.ID, child.ID)
	require.NoError(t, err)

	var count int64
	svc.DB.Model(&db.App{}).Where("id = ?", child.ID).Count(&count)
	assert.Zero(t, count, "the circle wallet child must be deleted")

	// The child's balance must be transferred back to the hub, not destroyed.
	hubBalance := queries.GetIsolatedBalance(svc.DB, hub.ID)
	assert.Greater(t, hubBalance, int64(0), "hub must gain balance after the reclaim transfer")
}

func TestDeleteCircleWalletChild_WrongHub_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hubA := newCircleHub(t, svc, 100_000, 3600)
	hubB := newCircleHub(t, svc, 100_000, 3600)
	child := createCircleChild(t, svc, hubA, "member")

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteCircleWalletChild(hubB.ID, child.ID)
	require.Error(t, err, "deleting another hub's child must fail")

	var count int64
	svc.DB.Model(&db.App{}).Where("id = ?", child.ID).Count(&count)
	assert.Equal(t, int64(1), count, "the child must still exist after a wrong-hub delete attempt")
}

func TestDeleteCircleWalletChild_NonCircleHub_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	isolatedApp, _, err := svc.AppsService.CreateApp(
		"iso", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteCircleWalletChild(isolatedApp.ID, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a circle_hub")
}
