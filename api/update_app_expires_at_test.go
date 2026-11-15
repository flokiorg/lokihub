package api

// F10 — UpdateApp updates AppPermission.ExpiresAt but does not update the
// denormalized App.ExpiresAt column.  The cleanup cron indexes on App.ExpiresAt,
// so wallets whose expiry was extended via UpdateApp continue to be swept on
// the old schedule, potentially deleting still-active sub-wallets.
//
// Correct behaviour: after UpdateApp changes ExpiresAt, the App.ExpiresAt
// column must match the new AppPermission.ExpiresAt value.
// This test FAILS today because App.ExpiresAt is never written by UpdateApp.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestUpdateApp_ExtendsExpiry_UpdatesAppExpiresAt(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Seed an isolated sub-wallet with an initial expiry.
	originalExpiry := time.Now().Add(24 * time.Hour).Truncate(time.Second).UTC()
	app := &db.App{
		Name:      "sub-wallet",
		Kind:      db.AppKindIsolated,
		ExpiresAt: &originalExpiry,
	}
	require.NoError(t, svc.DB.Create(app).Error)

	perm := &db.AppPermission{
		AppId:     app.ID,
		App:       *app,
		Scope:     constants.PAY_INVOICE_SCOPE,
		ExpiresAt: &originalExpiry,
	}
	require.NoError(t, svc.DB.Create(&perm).Error)

	// Extend the expiry by one week via UpdateApp.
	newExpiryTime := time.Now().Add(7 * 24 * time.Hour).Truncate(time.Second).UTC()
	newExpiryStr := newExpiryTime.Format(time.RFC3339)

	theAPI := newTestAPIWithEventPub(t, svc)
	err = theAPI.UpdateApp(app, &UpdateAppRequest{ExpiresAt: &newExpiryStr})
	require.NoError(t, err)

	// Assert AppPermission.ExpiresAt was updated (existing behaviour).
	var updatedPerm db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ?", app.ID).First(&updatedPerm).Error)
	require.NotNil(t, updatedPerm.ExpiresAt)
	assert.WithinDuration(t, newExpiryTime, *updatedPerm.ExpiresAt, time.Second,
		"AppPermission.ExpiresAt must be updated")

	// Assert App.ExpiresAt was also updated — the cron index depends on this.
	var updatedApp db.App
	require.NoError(t, svc.DB.First(&updatedApp, app.ID).Error)
	require.NotNil(t, updatedApp.ExpiresAt,
		"App.ExpiresAt must be non-nil after UpdateApp extends expiry")
	assert.WithinDuration(t, newExpiryTime, *updatedApp.ExpiresAt, time.Second,
		"App.ExpiresAt must mirror AppPermission.ExpiresAt after UpdateApp; "+
			"the cron cleanup index uses this column to sweep expired sub-wallets")
}

func TestUpdateApp_ClearsExpiry_UpdatesAppExpiresAt(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	expiry := time.Now().Add(24 * time.Hour).Truncate(time.Second).UTC()
	app := &db.App{Name: "sub-wallet", Kind: db.AppKindIsolated, ExpiresAt: &expiry}
	require.NoError(t, svc.DB.Create(app).Error)

	perm := &db.AppPermission{AppId: app.ID, App: *app, Scope: constants.PAY_INVOICE_SCOPE, ExpiresAt: &expiry}
	require.NoError(t, svc.DB.Create(&perm).Error)

	theAPI := newTestAPIWithEventPub(t, svc)
	// UpdateExpiresAt=true + ExpiresAt=nil means "clear the expiry".
	err = theAPI.UpdateApp(app, &UpdateAppRequest{UpdateExpiresAt: true})
	require.NoError(t, err)

	var updatedApp db.App
	require.NoError(t, svc.DB.First(&updatedApp, app.ID).Error)
	assert.Nil(t, updatedApp.ExpiresAt,
		"App.ExpiresAt must be cleared when UpdateApp removes the expiry; "+
			"otherwise the cleanup cron still uses the stale value")
}
