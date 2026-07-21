package api

import (
	"testing"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/tests/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateApp_SuperuserScopeIncorrectPassword(t *testing.T) {
	cfg := mocks.NewMockConfig(t)
	cfg.On("CheckUnlockPassword", "").Return(false)
	theAPI := &api{svc: mocks.NewMockService(t), cfg: cfg}
	response, err := theAPI.CreateApp(&CreateAppRequest{
		Scopes: []string{constants.SUPERUSER_SCOPE},
	})

	assert.Nil(t, response)
	require.Error(t, err)
	assert.Equal(t, "incorrect unlock password to create app with superuser permission", err.Error())
}

func TestUpdateApp_CircleAdmin_Immutable(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Create a circle_admin app directly in the DB.
	app := &db.App{
		Name: "circle",
		Kind: db.AppKindCircleHub,
	}
	require.NoError(t, svc.DB.Create(app).Error)
	perm := &db.AppPermission{AppId: app.ID, App: *app, Scope: constants.GET_BALANCE_SCOPE}
	require.NoError(t, svc.DB.Create(&perm).Error)

	// Scope changes must still be rejected for circle_hub.
	theAPI := newTestAPIWithEventPub(t, svc)
	updateErr := theAPI.UpdateApp(app, &UpdateAppRequest{
		Scopes: []string{constants.GET_BALANCE_SCOPE, constants.SIGN_MESSAGE_SCOPE},
	})
	require.Error(t, updateErr)
	assert.Equal(t, constants.ErrKindImmutable, updateErr)
}
