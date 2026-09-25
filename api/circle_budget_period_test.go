package api

import (
	"testing"
	"time"

	"github.com/flokiorg/lokihub/apps"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// Local helpers: the circle fixtures live in the apps test package, which this
// one cannot import.
func newCircleHubForAPI(t *testing.T, svc *tests.TestService, policy string) *db.App {
	t.Helper()
	hub, _, err := svc.AppsService.CreateCircleHub(
		"test-circle-hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.PAY_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "test-identity", Policy: policy, ProviderPubkey: tests.RandomHex32()},
		db.CircleHubConfig{PerWalletMaxMloki: 1_000_000, MaxExpSecs: 3600},
	)
	require.NoError(t, err)
	return hub
}

func newCircleMemberForAPI(t *testing.T, svc *tests.TestService, hubID uint, name string, maxAmountLoki uint64) *db.App {
	t.Helper()
	member, _, err := svc.AppsService.CreateApp(
		name, "", maxAmountLoki, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &hubID, db.ParentKindCircle, nil,
	)
	require.NoError(t, err)
	return member
}

func spendAt(t *testing.T, svc *tests.TestService, appID uint, amountMloki uint64, at time.Time) {
	t.Helper()
	id := appID
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:       &id,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		State:       constants.TRANSACTION_STATE_SETTLED,
		AmountMloki: amountMloki,
		SettledAt:   &at,
		CreatedAt:   at,
		PaymentHash: tests.RandomHex32(),
	}).Error)
}

// The figure shown beside a member's budget cap must be spend in the CURRENT
// period, not all time.
//
// A cap renews; an all-time total does not. Put side by side they invite a
// comparison that says nothing — a member a year into a monthly cap looks
// like they have blown through it twelve times over when they are inside it
// every month.
func TestGetCircleHubStats_BudgetUsedIsCurrentPeriodNotAllTime(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	now := time.Now()
	hub := newCircleHubForAPI(t, svc, db.CirclePolicyAllowlist)
	member := newCircleMemberForAPI(t, svc, hub.ID, "alice", 500)

	// A monthly cap, so "this period" begins on the 1st.
	require.NoError(t, svc.DB.Model(&db.AppPermission{}).
		Where("app_id = ? AND scope = ?", member.ID, constants.PAY_INVOICE_SCOPE).
		Update("budget_renewal", constants.BUDGET_RENEWAL_MONTHLY).Error)

	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	lastPeriod := startOfMonth.AddDate(0, 0, -5)
	thisPeriod := startOfMonth.Add(time.Hour)
	if thisPeriod.After(now) {
		thisPeriod = now.Add(-time.Minute)
	}

	// created_at is what the budget window filters on, so it is set directly.
	spendAt(t, svc, member.ID, 70_000, lastPeriod)
	spendAt(t, svc, member.ID, 30_000, thisPeriod)

	stats, err := newTestAPI(svc).GetCircleHubStats(hub.ID)
	require.NoError(t, err)
	require.Len(t, stats.PerMember, 1)
	m := stats.PerMember[0]

	assert.EqualValues(t, 100_000, m.SpentMloki,
		"all-time spend legitimately includes both periods")
	assert.EqualValues(t, 30_000, m.BudgetUsedMloki,
		"the figure beside the cap must cover only the current period, which is what the cap governs")
}
