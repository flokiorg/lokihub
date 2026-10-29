package controllers

import (
	"context"
	"encoding/json"
	"github.com/flokiorg/lokihub/apps"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CW §Fees: a member's fee rate is fixed when their Circle Wallet is created.
// Joining records the Hub's rate at that moment on the new wallet.
func TestCreateCircleWallet_RecordsTheHubRateOnTheWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000)
	rate := 7_500
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{FeesPpm: &rate}))

	response := joinCircle(t, context.TODO(), svc, provider, nostr.GeneratePrivateKey())
	require.Nil(t, response.Error, "%+v", response.Error)

	var wallet db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).First(&wallet).Error)
	require.NotNil(t, wallet.CircleFeesPpm)
	assert.Equal(t, rate, *wallet.CircleFeesPpm)
}

// NIP-CW §Fees: a host changing the Hub's rate affects only new members. A member
// who joined at 1% keeps paying 1% after the Hub moves to 5%.
func TestCircleWallet_HubRateChange_LeavesTheMemberRate(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	wallet := newAuditUXCircleWalletApp(t, svc, 10_000, 140_000) // hub at 1%
	joined := 10_000
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Update("circle_fees_ppm", joined).Error)
	raised := 50_000
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(*wallet.ParentAppID, apps.CircleHubConfigPatch{FeesPpm: &raised}))

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(nip47PayInvoiceJson), nip47Request))
	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(&dbRequestEvent).Error)

	var published *models.Response
	NewTestNip47Controller(svc).
		HandlePayInvoiceEvent(context.TODO(), nip47Request, dbRequestEvent.ID, wallet, func(r *models.Response, _ nostr.Tags) {
			published = r
		}, nostr.Tags{})

	require.Nil(t, published.Error, "%+v", published.Error)
	result, ok := published.Result.(payResponse)
	require.True(t, ok)
	assert.Equal(t, uint64(1_230), result.FeeSkimMloki, "1% of 123,000: the rate the member joined at, not the Hub's new 5%")
}
