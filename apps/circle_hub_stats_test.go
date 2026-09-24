package apps_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func newCircleHub(t *testing.T, svc *tests.TestService, policy string) *db.App {
	t.Helper()
	hub, _, err := svc.AppsService.CreateCircleHub(
		"test-circle-hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.PAY_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test-identity", Policy: policy, ProviderPubkey: randomHex32()},
		db.CircleHubConfig{PerWalletMaxMloki: 1_000_000, MaxExpSecs: 3600},
	)
	require.NoError(t, err)
	return hub
}

func newCircleMember(t *testing.T, svc *tests.TestService, hubID uint, name string, maxAmountLoki uint64) *db.App {
	t.Helper()
	member, _, err := svc.AppsService.CreateApp(
		name, "", maxAmountLoki, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &hubID, db.ParentKindCircle, nil,
	)
	require.NoError(t, err)
	return member
}

// settleTx writes a settled transaction against a member wallet, which is the
// only thing circle stats are derived from.
func settleTx(t *testing.T, svc *tests.TestService, appID uint, txType string, amountMloki, skimMloki uint64, at time.Time) {
	t.Helper()
	id := appID
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:        &id,
		Type:         txType,
		State:        constants.TRANSACTION_STATE_SETTLED,
		AmountMloki:  amountMloki,
		FeeSkimMloki: skimMloki,
		SettledAt:    &at,
		PaymentHash:  randomHex32(),
	}).Error)
}

// A circle hub had no stats at all before this: its page showed a table of
// members and a generic usage card. These are the figures that page needed —
// who is in the circle, what they hold, what they have spent, and what the
// host earned for carrying it.
func TestGetCircleHubStats_TotalsMemberActivity(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	now := time.Now()
	hub := newCircleHub(t, svc, db.CirclePolicyAllowlist)
	alice := newCircleMember(t, svc, hub.ID, "alice", 500)
	bob := newCircleMember(t, svc, hub.ID, "bob", 0)

	settleTx(t, svc, alice.ID, constants.TRANSACTION_TYPE_INCOMING, 100_000, 0, now.Add(-2*time.Hour))
	settleTx(t, svc, alice.ID, constants.TRANSACTION_TYPE_OUTGOING, 30_000, 300, now.Add(-time.Hour))
	settleTx(t, svc, bob.ID, constants.TRANSACTION_TYPE_OUTGOING, 10_000, 100, now.Add(-time.Hour))

	stats, err := svc.AppsService.GetCircleHubStats(hub.ID, now)
	require.NoError(t, err)

	assert.EqualValues(t, 2, stats.MembersCount)
	assert.EqualValues(t, 40_000, stats.SpentMloki, "both members' settled outgoing payments")
	assert.EqualValues(t, 2, stats.SpentCount)
	assert.EqualValues(t, 100_000, stats.ReceivedMloki)
	assert.EqualValues(t, 1, stats.ReceivedCount)
	assert.EqualValues(t, 400, stats.FeesEarnedMloki,
		"the host's cut is reported on its own, not folded into what members spent")

	// Biggest spender first, so the member worth looking at is the first bar.
	require.Len(t, stats.PerMember, 2)
	assert.Equal(t, alice.ID, stats.PerMember[0].WalletAppID)
	assert.EqualValues(t, 30_000, stats.PerMember[0].SpentMloki)
	assert.EqualValues(t, 500_000, stats.PerMember[0].MaxAmountMloki,
		"the App row stores a cap in loki while everything around it is mloki; the scaling must happen once, here")
	assert.Equal(t, bob.ID, stats.PerMember[1].WalletAppID)
	assert.EqualValues(t, 0, stats.PerMember[1].MaxAmountMloki, "an uncapped member reports 0, not a fabricated cap")

	// The series must sum back to the totals it sits beneath.
	var spent, received int64
	for _, d := range stats.Daily {
		spent += d.SpentMloki
		received += d.ReceivedMloki
	}
	assert.EqualValues(t, stats.SpentMloki, spent)
	assert.EqualValues(t, stats.ReceivedMloki, received)
}

// A member who has never spent must still appear. On a breakdown the question
// is "who is using this", and a member who drops out of the answer looks
// removed rather than idle.
func TestGetCircleHubStats_IncludesMembersWhoNeverSpent(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	now := time.Now()
	hub := newCircleHub(t, svc, db.CirclePolicyAllowlist)
	newCircleMember(t, svc, hub.ID, "idle", 100)

	stats, err := svc.AppsService.GetCircleHubStats(hub.ID, now)
	require.NoError(t, err)
	require.Len(t, stats.PerMember, 1)
	assert.EqualValues(t, 0, stats.PerMember[0].SpentMloki)
	assert.EqualValues(t, 1, stats.MembersCount)
}

// Only an allowlist circle can report how many pubkeys are eligible. A
// "following" circle draws its members from the host's live contact list,
// which this hub does not store and is not told about when it changes — so
// the honest answer is "not knowable here", never a stale number or a zero
// that would read as "nobody is eligible".
func TestGetCircleHubStats_EligibleCountOnlyForAllowlist(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	now := time.Now()

	following := newCircleHub(t, svc, db.CirclePolicyFollowing)
	stats, err := svc.AppsService.GetCircleHubStats(following.ID, now)
	require.NoError(t, err)
	assert.Nil(t, stats.EligibleCount,
		"a following-policy circle must report unknown rather than zero")

	allowlist := newCircleHub(t, svc, db.CirclePolicyAllowlist)
	allowStats, err := svc.AppsService.GetCircleHubStats(allowlist.ID, now)
	require.NoError(t, err)
	require.NotNil(t, allowStats.EligibleCount)
	assert.EqualValues(t, 0, *allowStats.EligibleCount,
		"an empty allowlist is a known zero, which is different from unknown")
}

// Pending payments must not count. A payment that has not left yet is not
// spent, and counting it would make the figure jump and fall back whenever one
// failed.
func TestGetCircleHubStats_IgnoresUnsettledPayments(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	now := time.Now()
	hub := newCircleHub(t, svc, db.CirclePolicyAllowlist)
	member := newCircleMember(t, svc, hub.ID, "alice", 500)

	settleTx(t, svc, member.ID, constants.TRANSACTION_TYPE_OUTGOING, 5_000, 0, now.Add(-time.Hour))
	id := member.ID
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:       &id,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		State:       constants.TRANSACTION_STATE_PENDING,
		AmountMloki: 99_000,
		PaymentHash: randomHex32(),
	}).Error)

	stats, err := svc.AppsService.GetCircleHubStats(hub.ID, now)
	require.NoError(t, err)
	assert.EqualValues(t, 5_000, stats.SpentMloki, "only the settled payment counts")
	assert.EqualValues(t, 1, stats.SpentCount)
}
