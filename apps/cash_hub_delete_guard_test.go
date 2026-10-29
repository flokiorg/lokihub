package apps_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// watchedOrder is a long-closed on-chain order of hub: it keeps nothing but the
// watch on its address.
func watchedOrder(t *testing.T, svc *tests.TestService, hubID uint) {
	t.Helper()
	old := time.Now().Add(-60 * 24 * time.Hour).Unix()
	addr := "fc1q" + tests.RandomHex32()[:38]
	require.NoError(t, svc.DB.Create(&db.CashOrder{
		RequestID: tests.RandomHex32(), HubAppID: hubID, OrderConnAppID: 1, OrderConnPubkey: tests.RandomHex32(),
		BuyerPubkey: tests.RandomHex32(), RecipientType: db.CashIdentityPubkey, RecipientValue: tests.RandomHex32(),
		AmountMillis: 21_000, Payment: "onchain", Notify: tests.RandomHex32(), ArrivalRelay: "wss://r", TermsJSON: "{}",
		PayMillis: 21_000, PayBy: old, Deadline: old, Address: &addr, State: db.CashOrderClosed, CreatedAt: old, StateAt: old,
	}).Error)
}

// NIP-CASH §Lifecycle and Deletion, Hub teardown (CASH-445, SHOULD): deleting a
// Hub ends the watch on its paid on-chain order addresses, so the owner
// confirms it first; with nothing watched no confirmation is asked.
func TestDeleteCashHub_WatchEndNeedsTheOwnersConfirmation(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	watchedOrder(t, svc, hub.ID)

	check, err := svc.AppsService.CashHubDeleteCheck(hub.ID)
	require.NoError(t, err)
	require.False(t, check.Blocked(), "only the watch remains: %s", check.Message())
	assert.EqualValues(t, 1, check.WatchedAddresses)
	assert.True(t, check.ConfirmationRequired)

	assert.ErrorIs(t, svc.AppsService.DeleteApp(hub), apps.ErrWatchEndUnconfirmed, "the generic delete asks too")
	assert.ErrorIs(t, svc.AppsService.DeleteCashHub(hub, false), apps.ErrWatchEndUnconfirmed)
	require.NotNil(t, svc.AppsService.GetAppById(hub.ID), "the hub stands")
	require.NoError(t, svc.AppsService.DeleteCashHub(hub, true))
	assert.Nil(t, svc.AppsService.GetAppById(hub.ID))

	plain := tests.CreateCashHub(t, svc, 100_000, 3600)
	check, err = svc.AppsService.CashHubDeleteCheck(plain.ID)
	require.NoError(t, err)
	assert.False(t, check.ConfirmationRequired)
	require.NoError(t, svc.AppsService.DeleteApp(plain), "nothing watched, nothing to confirm")
}

// WP-16 item 9: the delete check reports the Hub's listed offers, which the
// delete closes, as information and never as a blocker.
func TestCashHubDeleteCheck_ReportsListedOffersWithoutBlocking(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	later := time.Now().Add(20 * 24 * time.Hour).Unix()
	sooner := time.Now().Add(5 * 24 * time.Hour).Unix()
	for _, l := range []db.OfferListing{
		{Type: db.ListingTypeCashOrder, HubAppID: hub.ID, ConnAppID: 1, D: tests.RandomHex32(), Status: "open", Phase: db.ListingPhaseListed, LastOpenExpiresAt: &later},
		{Type: db.ListingTypeCashOrder, HubAppID: hub.ID, ConnAppID: 2, D: tests.RandomHex32(), Status: "open", Phase: db.ListingPhaseListed, LastOpenExpiresAt: &sooner},
		{Type: db.ListingTypeCashOrder, HubAppID: hub.ID, ConnAppID: 3, D: tests.RandomHex32(), Status: "closed", Phase: db.ListingPhaseClosing, LastOpenExpiresAt: &later},
	} {
		require.NoError(t, svc.DB.Create(&l).Error)
	}

	check, err := svc.AppsService.CashHubDeleteCheck(hub.ID)
	require.NoError(t, err)
	assert.False(t, check.Blocked())
	assert.EqualValues(t, 2, check.ListedOffers, "only listed ones")
	require.NotNil(t, check.ListingsDeleteDueAt)
	assert.Equal(t, later, *check.ListingsDeleteDueAt, "the last deletion follows the latest open version's expiry")
	require.NoError(t, svc.AppsService.DeleteApp(hub))

	empty := tests.CreateCashHub(t, svc, 100_000, 3600)
	check, err = svc.AppsService.CashHubDeleteCheck(empty.ID)
	require.NoError(t, err)
	assert.Zero(t, check.ListedOffers)
	assert.Nil(t, check.ListingsDeleteDueAt)
}
