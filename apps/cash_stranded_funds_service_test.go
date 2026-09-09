package apps_test

import (
	"testing"

	"github.com/flokiorg/lokihub/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCashStrandedFund_RecordListResolve exercises the full lifecycle: record
// two entries, list unresolved-only vs. all, resolve one, and confirm it's
// excluded from the unresolved filter afterward while still present overall.
func TestCashStrandedFund_RecordListResolve(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	require.NoError(t, svc.AppsService.RecordCashStrandedFund("consolidate", 10, 20, 123_000))
	require.NoError(t, svc.AppsService.RecordCashStrandedFund("split", 11, 21, 45_000))

	all, err := svc.AppsService.ListCashStrandedFunds(false)
	require.NoError(t, err)
	require.Len(t, all, 2)
	// Newest first.
	assert.Equal(t, "split", all[0].Operation)
	assert.EqualValues(t, 11, all[0].SourceWalletAppID)
	assert.EqualValues(t, 21, all[0].RetainedWalletAppID)
	assert.EqualValues(t, 45_000, all[0].AmountMloki)
	assert.Nil(t, all[0].ResolvedAt)
	assert.Equal(t, "consolidate", all[1].Operation)
	assert.EqualValues(t, 10, all[1].SourceWalletAppID)
	assert.EqualValues(t, 20, all[1].RetainedWalletAppID)
	assert.EqualValues(t, 123_000, all[1].AmountMloki)

	unresolved, err := svc.AppsService.ListCashStrandedFunds(true)
	require.NoError(t, err)
	require.Len(t, unresolved, 2)

	require.NoError(t, svc.AppsService.ResolveCashStrandedFund(all[1].ID)) // resolve the consolidate entry

	unresolvedAfter, err := svc.AppsService.ListCashStrandedFunds(true)
	require.NoError(t, err)
	require.Len(t, unresolvedAfter, 1)
	assert.Equal(t, "split", unresolvedAfter[0].Operation)

	allAfter, err := svc.AppsService.ListCashStrandedFunds(false)
	require.NoError(t, err)
	require.Len(t, allAfter, 2, "resolving must not delete the record, only mark it")
	for _, r := range allAfter {
		if r.Operation == "consolidate" {
			require.NotNil(t, r.ResolvedAt)
		}
	}
}

// TestCashStrandedFund_ResolveIdempotent asserts resolving an already-resolved
// (or nonexistent) record is a no-op, not an error — an operator retrying the
// same sweep-confirmation action shouldn't fail.
func TestCashStrandedFund_ResolveIdempotent(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	require.NoError(t, svc.AppsService.RecordCashStrandedFund("consolidate", 1, 2, 1000))
	all, err := svc.AppsService.ListCashStrandedFunds(false)
	require.NoError(t, err)
	require.Len(t, all, 1)

	require.NoError(t, svc.AppsService.ResolveCashStrandedFund(all[0].ID))
	require.NoError(t, svc.AppsService.ResolveCashStrandedFund(all[0].ID)) // already resolved: no-op
	require.NoError(t, svc.AppsService.ResolveCashStrandedFund(99999))     // nonexistent: no-op
}
