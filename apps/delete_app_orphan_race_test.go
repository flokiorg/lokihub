package apps_test

// These tests cover the fix for the orphan race found in code review:
// DeleteApp's circle_hub/jit_hub child-count guard used to be a
// non-transactional check-then-act (a separate Count() then a separate
// Delete()), so a circle_wallet/jit_wallet creation that committed in the gap
// between the two could still be orphaned by the delete that follows
// (App.ParentAppID has no DB-level cascade). The fix wraps the count and the
// delete in one transaction (deleteHubAppTx) and adds a matching check on the
// child-creation side (verifyParentHubTx, called from saveAppTx) so a child
// creation racing a concurrent hub deletion fails cleanly instead of landing
// as an orphan.

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestCreateApp_ParentNotFound_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	bogusParentID := uint(999_999)
	child, _, err := svc.AppsService.CreateApp(
		"child", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindJITWallet,
		&bogusParentID, db.ParentKindJIT, nil,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
	assert.Nil(t, child)
}

func TestCreateApp_ParentWrongKind_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// A plain isolated app is not a jit_hub, so it must not be usable as a
	// jit_wallet's parent even though it's a real, existing app row.
	notAHub, _, err := svc.AppsService.CreateApp(
		"not-a-hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)

	child, _, err := svc.AppsService.CreateApp(
		"child", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindJITWallet,
		&notAHub.ID, db.ParentKindJIT, nil,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
	assert.Nil(t, child)
}

// TestDeleteAppAndCreateChild_ConcurrentRace_NeverOrphans is the regression
// test for the orphan race itself: race a jit_wallet creation under hub
// against DeleteApp(hub) on an initially childless hub, many times, and
// assert the hub and its (possible) child are never left in an inconsistent
// state — either the hub is gone and no child referencing it exists, or the
// hub survived (because a child got in first) and that child is intact.
func TestDeleteAppAndCreateChild_ConcurrentRace_NeverOrphans(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const trials = 100
	for trial := 0; trial < trials; trial++ {
		hub, _, err := svc.AppsService.CreateJITHub(
			"race-hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
			[]string{constants.JIT_HUB_SCOPE, constants.GET_BALANCE_SCOPE}, nil,
			db.JITHubConfig{PerWalletMaxMloki: 100_000, MaxExpSecs: 3600},
		)
		require.NoError(t, err)

		ready := make(chan struct{})
		var wg sync.WaitGroup
		var createErr, deleteErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-ready
			_, _, createErr = svc.AppsService.CreateApp(
				"race-child", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
				[]string{constants.GET_BALANCE_SCOPE}, db.AppKindJITWallet,
				&hub.ID, db.ParentKindJIT, nil,
			)
		}()
		go func() {
			defer wg.Done()
			<-ready
			deleteErr = svc.AppsService.DeleteApp(hub)
		}()
		close(ready)
		wg.Wait()

		hubStillExists := svc.AppsService.GetAppById(hub.ID) != nil
		var orphanCount int64
		require.NoError(t, svc.DB.Model(&db.App{}).
			Where("parent_app_id = ? AND parent_kind = ?", hub.ID, db.ParentKindJIT).
			Count(&orphanCount).Error)

		if !hubStillExists {
			require.Zerof(t, orphanCount, "trial %d: hub was deleted but a child still references it as parent — orphan", trial)
		}

		// Exactly one of the two operations should have won on a childless hub:
		// either the child landed first (delete must then be refused, hub survives),
		// or the delete landed first (create must then fail against a gone parent).
		createSucceeded := createErr == nil
		deleteSucceeded := deleteErr == nil
		assert.NotEqualf(t, createSucceeded, deleteSucceeded,
			"trial %d: create and delete must not both succeed or both fail on a childless hub (createErr=%v, deleteErr=%v)",
			trial, createErr, deleteErr)

		// Clean up whatever is left before the next trial.
		if hubStillExists {
			var children []db.App
			svc.DB.Where("parent_app_id = ? AND parent_kind = ?", hub.ID, db.ParentKindJIT).Find(&children)
			for _, c := range children {
				require.NoError(t, svc.DB.Delete(&c).Error)
			}
			require.NoError(t, svc.DB.Delete(hub).Error)
		} else if orphanCount > 0 {
			require.NoError(t, svc.DB.Where("parent_app_id = ? AND parent_kind = ?", hub.ID, db.ParentKindJIT).Delete(&db.App{}).Error)
		}
	}
}
