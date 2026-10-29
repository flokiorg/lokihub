package api

// Covers DeleteCircleWalletChild, the host's delete of one member wallet: it
// moves the wallet to Closing (NIP-CW §Lifecycle and Deletion) and never moves
// its balance; the wallet goes once its grace is over and it holds at most dust.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/circlelife"
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

// A host delete moves the wallet to Closing, starting its grace, and frees the
// member to join again; its balance stays where it is (U-16.2).
func TestDeleteCircleWalletChild_MovesToClosing_BalanceUntouched(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 300_000, 3600)
	child := createCircleChild(t, svc, hub, "member")
	require.NoError(t, svc.DB.Create(&db.CircleWalletMembership{CircleHubAppID: hub.ID, RequesterPubkey: tests.RandomHex32(), WalletAppID: child.ID}).Error)
	tests.FundApp(svc, child.ID, 200_000, tests.RandomHex32())

	before := time.Now().Unix()
	require.NoError(t, newTestAPIWithService(t, svc).DeleteCircleWalletChild(hub.ID, child.ID))

	var closing db.App
	require.NoError(t, svc.DB.First(&closing, child.ID).Error, "the wallet stays, paying out only")
	assert.Equal(t, circlelife.StateClosing, closing.CircleState)
	require.NotNil(t, closing.CircleGraceEndsAt)
	assert.GreaterOrEqual(t, *closing.CircleGraceEndsAt, before+constants.CIRCLE_DEFAULT_GRACE_SECS)
	assert.Equal(t, int64(200_000), queries.GetIsolatedBalance(svc.DB, child.ID), "nothing is swept")
	assert.Zero(t, queries.GetIsolatedBalance(svc.DB, hub.ID))
	var memberships int64
	require.NoError(t, svc.DB.Model(&db.CircleWalletMembership{}).Where("wallet_app_id = ?", child.ID).Count(&memberships).Error)
	assert.Zero(t, memberships, "a Closing wallet no longer counts for the one-wallet rule")

	// Again: nothing changes, the grace is not restarted (U-16.9).
	graceEnds := *closing.CircleGraceEndsAt
	require.NoError(t, newTestAPIWithService(t, svc).DeleteCircleWalletChild(hub.ID, child.ID))
	require.NoError(t, svc.DB.First(&closing, child.ID).Error)
	assert.Equal(t, graceEnds, *closing.CircleGraceEndsAt)
}
