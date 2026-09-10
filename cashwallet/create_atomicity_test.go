package cashwallet

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// failingCommitAppsService wraps a real apps.AppsService and can be told to
// fail CreateCashWalletClaimsTx and/or DeleteApp a bounded number of times.
// Used to prove two things about Commit()'s atomicity fix:
//   - a failure while creating the wallet app/claims (before any funds move)
//     can no longer strand a partial row, even if the (now unreachable, for
//     this window) compensating delete would also fail — see
//     TestCommit_ClaimsInsertFails_DeleteAppAlsoFails_NoStrayRows.
//   - a failure funding an already-committed wallet still gets cleaned up
//     even if the first attempt(s) at the compensating delete fail — see
//     TestCommit_FundInternalFails_DeleteAppRetrySucceedsAfterTransientFailure.
type failingCommitAppsService struct {
	apps.AppsService
	failClaimsTx     bool
	failDeleteNTimes int
	deleteCallCount  int
}

func (f *failingCommitAppsService) CreateCashWalletClaimsTx(tx *gorm.DB, walletAppID uint, entries []db.CashWalletClaim) error {
	if f.failClaimsTx {
		return errors.New("injected: claims insert failed")
	}
	return f.AppsService.CreateCashWalletClaimsTx(tx, walletAppID, entries)
}

func (f *failingCommitAppsService) DeleteApp(app *db.App) error {
	f.deleteCallCount++
	if f.deleteCallCount <= f.failDeleteNTimes {
		return errors.New("injected: compensating delete failed")
	}
	return f.AppsService.DeleteApp(app)
}

func countCashWalletAppsAndClaims(t *testing.T, svc *tests.TestService) (apps int64, claims int64) {
	t.Helper()
	require.NoError(t, svc.DB.Model(&db.App{}).Where("kind = ?", db.AppKindCashWallet).Count(&apps).Error)
	require.NoError(t, svc.DB.Model(&db.CashWalletClaim{}).Count(&claims).Error)
	return apps, claims
}

// TestCommit_ClaimsInsertFails_DeleteAppAlsoFails_NoStrayRows is the direct
// regression test for the conformance-audit finding (2026-09-10): before the
// transactional fix, forcing the claims insert to fail *and* the compensating
// DeleteApp to also fail left a real, fully-permissioned, unfunded,
// claim-less cash_wallet app row stranded and observable — contradicting
// NIP-CASH.md's unconditional "no partial wallet... ever observable" claim
// for mint_cash. Now that app creation + claim insertion happen in one DB
// transaction, a claims-insert failure rolls the whole thing back at the DB
// engine level, so DeleteApp is never even reached for this window — this
// test forces it to fail anyway, to prove that no longer matters.
func TestCommit_ClaimsInsertFails_DeleteAppAlsoFails_NoStrayRows(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	deps := newTestDeps(svc)
	failing := &failingCommitAppsService{AppsService: deps.AppsService, failClaimsTx: true, failDeleteNTimes: 999}
	deps.AppsService = failing

	appsBefore, claimsBefore := countCashWalletAppsAndClaims(t, svc)

	_, err = Create(context.TODO(), deps, Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		ExpirySecs: 1800,
	})
	require.Error(t, err, "expected CreateCashWalletClaimsTx to fail as injected")

	appsAfter, claimsAfter := countCashWalletAppsAndClaims(t, svc)
	assert.Equal(t, appsBefore, appsAfter,
		"a claims-insert failure must leave zero new cash_wallet app rows — the transaction rolls back "+
			"the app creation too, so the (here permanently-failing) compensating delete is never needed")
	assert.Equal(t, claimsBefore, claimsAfter, "no claim rows should exist either")
	assert.Equal(t, 0, failing.deleteCallCount,
		"DeleteApp should never even be called for a pre-funding failure now that it's covered by the transaction")
}

// TestCommit_FundInternalFails_DeleteAppRetrySucceedsAfterTransientFailure
// covers the narrower residual window Part A can't remove: fundInternal
// (a real payment) can't safely live inside the DB transaction, so if it
// fails after the transaction already committed, cleanup still needs a
// compensating delete. This proves that delete is now retried instead of
// attempted once and silently discarded — a transient failure (here, the
// first two attempts) no longer permanently stops cleanup.
func TestCommit_FundInternalFails_DeleteAppRetrySucceedsAfterTransientFailure(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.PayInvoiceResponses = []*lnclient.PayInvoiceResponse{nil}
	mockLN.PayInvoiceErrors = []error{errors.New("simulated payment failure")}

	deps := newTestDeps(svc)
	failing := &failingCommitAppsService{AppsService: deps.AppsService, failDeleteNTimes: compensatingDeleteMaxAttempts - 1}
	deps.AppsService = failing

	_, err = Create(context.TODO(), deps, Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		ExpirySecs: 1800,
	})
	require.Error(t, err, "expected the funding transfer to fail as injected")

	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&childApps)
	assert.Empty(t, childApps, "the compensating delete must still succeed within its retry budget")

	var claims []db.CashWalletClaim
	svc.DB.Find(&claims)
	assert.Empty(t, claims, "claim rows must be cleaned up too (FK cascade on the deleted app)")

	assert.Equal(t, compensatingDeleteMaxAttempts, failing.deleteCallCount,
		"expected exactly %d DeleteApp attempts: %d injected failures then one success",
		compensatingDeleteMaxAttempts, compensatingDeleteMaxAttempts-1)
}
