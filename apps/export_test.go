package apps

import (
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// Test-only handles on the two union queries, so the derivation guard in
// cash_hub_stats_all_test.go can compare them from the external test package
// without exporting either into the real API.
const (
	ExportedCashClaimUnionSQL = cashClaimUnionSQL
)

var ExportedAllHubsCashClaimUnionSQL = allHubsCashClaimUnionSQL

// ExportedSaveAppTx runs saveAppTx alone, without prepareApp's duplicate-pubkey
// pre-check, so a test can see what the database itself does with a duplicate.
func (svc *appsService) ExportedSaveAppTx(app *db.App, scopes []string) error {
	return svc.db.Transaction(func(tx *gorm.DB) error {
		return svc.saveAppTx(tx, app, scopes, 0, constants.BUDGET_RENEWAL_NEVER, nil)
	})
}
