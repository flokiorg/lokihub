package api

import (
	"context"
	"errors"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/listing"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/tests/mocks"
)

// U-25.9: "Regenerate connection" replaces the client key at once, issues the new
// string once, keeps the hub group input and leaves the bills untouched.
func TestRegenerateCashHubConnection(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	theAPI := newTestAPIWithService(t, svc)

	resp, err := theAPI.CreateApp(&CreateAppRequest{Name: "Hub", Kind: db.AppKindCashHub,
		Scopes: []string{constants.CASH_HUB_SCOPE, constants.GET_BALANCE_SCOPE}, CashPerWalletMaxMloki: 100_000, CashMaxExpSecs: 3600})
	require.NoError(t, err)
	var before db.App
	require.NoError(t, svc.DB.First(&before, resp.Id).Error)
	oldKey := before.AppPubkey

	out, err := theAPI.RegenerateCashHubConnection(context.Background(), resp.Id)
	require.NoError(t, err)
	decoded, err := nipcash.DecodeCashHubConnection(out.CashHubToken)
	require.NoError(t, err)
	require.NoError(t, decoded.Verify())
	newKey, err := nostr.GetPublicKey(decoded.Secret)
	require.NoError(t, err)

	var after db.App
	require.NoError(t, svc.DB.First(&after, resp.Id).Error)
	assert.Equal(t, newKey, after.AppPubkey)
	assert.NotEqual(t, oldKey, after.AppPubkey)
	require.NotNil(t, after.HubGroupPubkey)
	assert.Equal(t, oldKey, *after.HubGroupPubkey, "the hub group input never changes")
	assert.Equal(t, *before.WalletPubkey, *after.WalletPubkey)
	var n int64
	svc.DB.Model(&db.App{}).Where("app_pubkey = ?", oldKey).Count(&n)
	assert.Zero(t, n, "the old client key resolves to no app")

	// Twice: the hub group input stays the first key.
	_, err = theAPI.RegenerateCashHubConnection(context.Background(), resp.Id)
	require.NoError(t, err)
	require.NoError(t, svc.DB.First(&after, resp.Id).Error)
	assert.Equal(t, oldKey, *after.HubGroupPubkey)

	// Not a Cash Hub.
	_, err = theAPI.RegenerateCashHubConnection(context.Background(), 999999)
	assert.True(t, errors.Is(err, listing.ErrNotFound))
}

// U-25.1: the API refuses a body carrying any connection string before anything
// else, and answers ErrNodeNotStarted while the node is not started.
func TestCreateListing_RefusesStrings(t *testing.T) {
	mockSvc := mocks.NewMockService(t)
	mockSvc.On("GetListingService").Return(nil).Maybe()
	theAPI := &api{svc: mockSvc}
	_, err := theAPI.CreateListing(context.Background(), []byte(`{"type":"circle_wallet","connection":"circlehub1abc"}`))
	assert.ErrorIs(t, err, listing.ErrRefused)
	_, err = theAPI.CreateListing(context.Background(), []byte(`{"conn_app_id":1,"title":"CASHHUB1QQQ"}`))
	assert.ErrorIs(t, err, listing.ErrRefused)
	_, err = theAPI.CreateListing(context.Background(), []byte(`{"circle_hub_app_id":1}`))
	assert.ErrorIs(t, err, ErrNodeNotStarted)
}
