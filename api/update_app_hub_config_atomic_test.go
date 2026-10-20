package api

// A hub settings update is all or nothing: a rejected hub setting must not
// leave the name or budget change from the same request saved.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func requireAppUnchanged(t *testing.T, svc *tests.TestService, app *db.App, budgetLoki int) {
	t.Helper()
	var stored db.App
	require.NoError(t, svc.DB.First(&stored, app.ID).Error)
	assert.Equal(t, app.Name, stored.Name, "the name change must not be saved")
	var perm db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", app.ID, constants.PAY_INVOICE_SCOPE).First(&perm).Error)
	assert.Equal(t, budgetLoki, perm.MaxAmountLoki, "the budget change must not be saved")
}

func TestUpdateApp_CircleHub_RejectedSettingSavesNothing(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 100_000, 3600)
	require.NoError(t, svc.DB.Model(&db.AppPermission{}).Where("app_id = ?", hub.ID).
		Updates(map[string]interface{}{"scope": constants.PAY_INVOICE_SCOPE, "max_amount_loki": 500}).Error)

	newName := "renamed circle"
	newBudget := uint64(900)
	badFees := constants.MAX_FEES_PPM + 1
	err = newTestAPIWithEventPub(t, svc).UpdateApp(hub, &UpdateAppRequest{
		Name: &newName, MaxAmountLoki: &newBudget, CircleFeesPpm: &badFees,
	})
	require.ErrorIs(t, err, constants.ErrInvalidParams)
	requireAppUnchanged(t, svc, hub, 500)
}

func TestUpdateApp_CashHub_RejectedSettingSavesNothing(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	require.NoError(t, svc.DB.Model(&db.AppPermission{}).Where("app_id = ? AND scope = ?", hub.ID, constants.PAY_INVOICE_SCOPE).
		Update("max_amount_loki", 500).Error)

	newName := "renamed cash hub"
	newBudget := uint64(900)
	badFees := constants.MAX_FEES_PPM + 1
	err = newTestAPIWithEventPub(t, svc).UpdateApp(hub, &UpdateAppRequest{
		Name: &newName, MaxAmountLoki: &newBudget, CashRedeemFeePpm: &badFees,
	})
	require.ErrorIs(t, err, constants.ErrInvalidParams)
	requireAppUnchanged(t, svc, hub, 500)
}
