package queries

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestGetCircleCommitmentMloki_NoChildren(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("parent", "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)

	sum, err := GetCircleCommitmentMloki(svc.DB, parent.ID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), sum)
}

func TestGetCircleCommitmentMloki_ActiveChildren(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("parent", "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)

	future := time.Now().Add(time.Hour)

	// child1: max 100 loki = 100_000 mloki
	_, _, err = svc.AppsService.CreateApp("child1", "", 100, "never", &future, []string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)

	// child2: max 50 loki = 50_000 mloki
	_, _, err = svc.AppsService.CreateApp("child2", "", 50, "never", &future, []string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)

	sum, err := GetCircleCommitmentMloki(svc.DB, parent.ID)
	assert.NoError(t, err)
	assert.Equal(t, int64(150_000), sum)
}

func TestGetCircleCommitmentMloki_ExpiredChildrenNotCounted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("parent", "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	// active child: 80 loki = 80_000 mloki
	_, _, err = svc.AppsService.CreateApp("active", "", 80, "never", &future, []string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)

	// expired child: 200 loki — should NOT be counted
	_, _, err = svc.AppsService.CreateApp("expired", "", 200, "never", &past, []string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)

	sum, err := GetCircleCommitmentMloki(svc.DB, parent.ID)
	assert.NoError(t, err)
	assert.Equal(t, int64(80_000), sum)
}

func TestGetCircleCommitmentMloki_NullExpiryCountedAsActive(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("parent", "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)

	// child with no expiry (nil) — counts as permanently active
	_, _, err = svc.AppsService.CreateApp("permanent", "", 60, "never", nil, []string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)

	sum, err := GetCircleCommitmentMloki(svc.DB, parent.ID)
	assert.NoError(t, err)
	assert.Equal(t, int64(60_000), sum)
}

func TestGetCircleCommitmentMloki_IsolatesPerParent(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent1, _, err := svc.AppsService.CreateApp("parent1", "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)
	parent2, _, err := svc.AppsService.CreateApp("parent2", "", 0, "never", nil, []string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)

	future := time.Now().Add(time.Hour)
	_, _, err = svc.AppsService.CreateApp("p1-child", "", 100, "never", &future, []string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &parent1.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)
	_, _, err = svc.AppsService.CreateApp("p2-child", "", 999, "never", &future, []string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &parent2.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)

	sum1, err := GetCircleCommitmentMloki(svc.DB, parent1.ID)
	assert.NoError(t, err)
	assert.Equal(t, int64(100_000), sum1)

	sum2, err := GetCircleCommitmentMloki(svc.DB, parent2.ID)
	assert.NoError(t, err)
	assert.Equal(t, int64(999_000), sum2)
}
