package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

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
		testJoin(t, context.TODO(), nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			published = r
		})
	require.NotNil(t, published)
	return published, requesterKey
}

// At the fee reserve plus 1 loki the member can pay 1 loki out over Lightning
// (a smaller cap is accepted too: TestHandleCreateCircleWalletEvent_BelowFeeReserve_Accepted).
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
	require.NoError(t, err, "a wallet capped at the reserve plus 1 loki pays 1 loki out")
}
