package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// The circle dashboard's headline figures were asserted nowhere in the repo.
//
// AllocatedMloki and each member's balance are computed in the api layer,
// from the same helper every other balance on the node uses, and the cash
// side has an equivalent test. This is the circle side's.
func TestGetCircleHubStats_AllocatedIsTheSumOfMemberBalances(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHubForAPI(t, svc, db.CirclePolicyAllowlist)
	alice := newCircleMemberForAPI(t, svc, hub.ID, "alice", 500)
	bob := newCircleMemberForAPI(t, svc, hub.ID, "bob", 0)
	idle := newCircleMemberForAPI(t, svc, hub.ID, "idle", 100)

	fundApp(t, svc, alice.ID, 60_000)
	fundApp(t, svc, bob.ID, 40_000)
	// idle is funded with nothing on purpose.

	stats, err := newTestAPI(svc).GetCircleHubStats(hub.ID)
	require.NoError(t, err)

	assert.EqualValues(t, 3, stats.MembersCount)
	assert.EqualValues(t, 100_000, stats.AllocatedMloki,
		"allocated is what members are holding between them")

	byID := map[uint]int64{}
	for _, m := range stats.PerMember {
		byID[m.WalletAppID] = m.BalanceMloki
	}
	assert.EqualValues(t, 60_000, byID[alice.ID])
	assert.EqualValues(t, 40_000, byID[bob.ID])
	assert.EqualValues(t, 0, byID[idle.ID],
		"a member holding nothing still appears, or the breakdown makes them look removed")

	var summed int64
	for _, m := range stats.PerMember {
		summed += m.BalanceMloki
	}
	assert.Equal(t, stats.AllocatedMloki, summed,
		"the per-member split must sum back to the headline figure above it")
}

// A member's cap comes from its pay_invoice permission and is stored in loki
// while every amount beside it is mloki. The scaling happens once, server
// side; an uncapped member reports 0 rather than a fabricated cap.
func TestGetCircleHubStats_MemberCapIsScaledOnceAndZeroMeansUncapped(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHubForAPI(t, svc, db.CirclePolicyAllowlist)
	capped := newCircleMemberForAPI(t, svc, hub.ID, "capped", 500)
	uncapped := newCircleMemberForAPI(t, svc, hub.ID, "uncapped", 0)

	stats, err := newTestAPI(svc).GetCircleHubStats(hub.ID)
	require.NoError(t, err)

	byID := map[uint]int64{}
	for _, m := range stats.PerMember {
		byID[m.WalletAppID] = m.MaxAmountMloki
	}
	assert.EqualValues(t, 500_000, byID[capped.ID], "500 loki is 500,000 mloki")
	assert.EqualValues(t, 0, byID[uncapped.ID], "0 means no cap, not a cap of nothing")
}

// A following-policy circle cannot report how many pubkeys are eligible: its
// members come from the host's live contact list, which this hub does not
// store. Null is the honest answer; zero would read as "nobody".
func TestGetCircleHubStats_EligibleCountIsNullForFollowingPolicy(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	following := newCircleHubForAPI(t, svc, db.CirclePolicyFollowing)
	stats, err := newTestAPI(svc).GetCircleHubStats(following.ID)
	require.NoError(t, err)
	assert.Nil(t, stats.EligibleCount)

	allowlist := newCircleHubForAPI(t, svc, db.CirclePolicyAllowlist)
	allowStats, err := newTestAPI(svc).GetCircleHubStats(allowlist.ID)
	require.NoError(t, err)
	require.NotNil(t, allowStats.EligibleCount)
	assert.EqualValues(t, 0, *allowStats.EligibleCount,
		"an empty allowlist is a known zero, which is a different fact from unknown")
}

// The endpoint refuses an app that is not a circle hub, rather than returning
// empty figures that would read as a circle with nothing in it.
func TestGetCircleHubStats_RejectsNonCircleHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	cashHub := tests.CreateCashHub(t, svc, 10_000, 3600)
	_, err = newTestAPI(svc).GetCircleHubStats(cashHub.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circle_hub")
}
