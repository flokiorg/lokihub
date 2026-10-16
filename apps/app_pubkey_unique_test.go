package apps_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// Two apps sharing an app_pubkey would route one's NWC requests to the other.
// prepareApp's check is read-then-insert, so two concurrent creations can both
// pass it; the database must refuse the second insert itself, and saveAppTx must
// report that as the same ErrDuplicateAppPubkey the pre-check gives.
func TestSaveApp_DuplicateAppPubkey_RefusedByTheDatabase(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	existing, _, err := svc.AppsService.CreateApp("first", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", nil)
	require.NoError(t, err)

	// A raw insert, bypassing every application check.
	rawErr := svc.DB.Create(&db.App{Name: "raw duplicate", AppPubkey: existing.AppPubkey, Kind: db.AppKindIsolated}).Error
	require.True(t, db.IsDuplicateKeyError(rawErr), "the database refuses a duplicate app_pubkey, got %v", rawErr)

	saver, ok := svc.AppsService.(interface {
		ExportedSaveAppTx(app *db.App, scopes []string) error
	})
	require.True(t, ok)
	err = saver.ExportedSaveAppTx(&db.App{Name: "second", AppPubkey: existing.AppPubkey, Kind: db.AppKindIsolated},
		[]string{constants.GET_INFO_SCOPE})
	require.ErrorIs(t, err, apps.ErrDuplicateAppPubkey, "a duplicate lost to a race is the pre-check's refusal")

	var count int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("app_pubkey = ?", existing.AppPubkey).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
