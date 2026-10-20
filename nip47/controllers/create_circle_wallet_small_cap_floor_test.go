package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// joinWithMaxAmount sends one create_circle_wallet request for a fresh member
// and returns the response.
func joinWithMaxAmount(t *testing.T, svc *tests.TestService, provider *db.App, maxAmount uint64) (*models.Response, string) {
	t.Helper()
	requesterKey := nostr.GeneratePrivateKey()
	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal(
		[]byte(makeCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, maxAmount, 3600)), nip47Request))
	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var published *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(context.TODO(), nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			published = r
		})
	require.NotNil(t, published)
	return published, requesterKey
}

// A cap at or below the 10-loki fee reserve leaves a wallet that can never pay
// outside the node, so joins below reserve + 1 loki are refused.
func TestHandleCreateCircleWalletEvent_MaxAmountBelowFeeReserveFloor_Rejected(t *testing.T) {
	for _, maxAmount := range []uint64{1_000, 5_000, 10_000, 10_999} {
		t.Run(fmt.Sprintf("max_amount_%d", maxAmount), func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			provider := createCircleHubWithCaps(t, svc, 3600, 10_000_000, 1_000_000_000, "")

			resp, _ := joinWithMaxAmount(t, svc, provider, maxAmount)
			require.NotNil(t, resp.Error, "a cap the fee reserve swallows must be refused")
			assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)

			var count int64
			require.NoError(t, svc.DB.Model(&db.App{}).
				Where("parent_app_id = ? AND parent_kind = ?", provider.ID, db.ParentKindCircle).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

// At the floor itself the member can pay 1 loki out over Lightning.
func TestHandleCreateCircleWalletEvent_MaxAmountAtFloor_CanPayOut(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHubWithCaps(t, svc, 3600, 10_000_000, 1_000_000_000, "")

	resp, requesterKey := joinWithMaxAmount(t, svc, provider, 11_000)
	require.Nil(t, resp.Error)
	result, ok := resp.Result.(createCircleWalletResponse)
	require.True(t, ok)
	details := decryptCircleWalletDetails(t, requesterKey, *provider.WalletPubkey, result.EncryptedDetails)

	var wallet db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", details.WalletPubkey).First(&wallet).Error)
	tests.FundApp(svc, wallet.ID, 11_000, tests.RandomHex32())

	amount := uint64(1_000)
	ts := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	_, err = ts.SendPaymentSync(tests.MockZeroAmountInvoice, &amount, nil, svc.LNClient, &wallet.ID, nil)
	require.NoError(t, err, "a wallet at the floor holds the reserve plus 1 loki")
}
