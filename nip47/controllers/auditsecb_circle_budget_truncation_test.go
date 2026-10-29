package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// The pay_invoice permission keeps a budget in whole loki, with 0 meaning "no
// cap at all". A sub-loki max_amount once truncated to that 0 and silently
// disabled a Circle Wallet's send cap (a 246x overspend, shown live). Joins
// used to be refused below a floor to close it, but NIP-CW §Processing
// Algorithm step 6 has no floor on max_amount and get_info no field to
// advertise one. A join now takes any max_amount, rounds it down to whole
// loki (never raising it, NIP-CW §Response), and stores the result exactly on
// the wallet (CircleMaxAmountMloki): a request under 1 loki is a cap of 0,
// never "no cap". These tests prove that cap holds.

// joinResolved joins a fresh member with maxAmount and returns the
// decrypted details and the wallet.
func joinResolved(t *testing.T, svc *tests.TestService, provider *db.App, maxAmount uint64) (uint64, db.App) {
	t.Helper()
	requesterKey := nostr.GeneratePrivateKey()
	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal(
		[]byte(makeCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, maxAmount, 3600)), nip47Request))
	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		testJoin(t, context.TODO(), nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})
	require.NotNil(t, publishedResponse)
	require.Nil(t, publishedResponse.Error, "a join is never refused for a small max_amount")
	resp, ok := publishedResponse.Result.(createCircleWalletResponse)
	require.True(t, ok)
	details := decryptCircleWalletDetails(t, requesterKey, *provider.WalletPubkey, resp.EncryptedDetails)

	var walletApp db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", details.WalletPubkey).First(&walletApp).Error)
	return details.MaxAmount, walletApp
}

// requireSendCapRefusal pays tests.MockInvoice (123,000 mloki) from a wallet
// funded with 200,000 mloki and requires QUOTA_EXCEEDED.
func requireSendCapRefusal(t *testing.T, svc *tests.TestService, wallet db.App) {
	t.Helper()
	tests.FundApp(svc, wallet.ID, 200_000, tests.RandomHex32())
	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	_, err := transactionsSvc.SendPaymentSync(tests.MockInvoice, nil, nil, svc.LNClient, &wallet.ID, nil)
	require.Error(t, err, "a capped circle_wallet must not pay 123,000 mloki, even funded beyond it")
	r, ok := circlemoney.AsRefusal(err)
	require.True(t, ok, "refused by the circle send checks: %v", err)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, r.Code)
}

func TestHandleCreateCircleWalletEvent_SubLokiMaxAmount_ZeroCap(t *testing.T) {
	for _, maxAmount := range []uint64{0, 1, 500, 999} {
		t.Run(fmt.Sprintf("max_amount_%d", maxAmount), func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			// High per-wallet ceiling and high balance so neither binds.
			provider := createCircleHubWithCaps(t, svc, 3600, 10_000_000, 1_000_000_000, "")

			reported, wallet := joinResolved(t, svc, provider, maxAmount)
			assert.Equal(t, uint64(0), reported, "rounded down to whole loki, never raised")
			require.NotNil(t, wallet.CircleMaxAmountMloki)
			assert.Equal(t, int64(0), *wallet.CircleMaxAmountMloki, "stored exactly: a cap of 0")

			caps, err := circlemoney.WalletCaps(svc.DB, &wallet)
			require.NoError(t, err)
			assert.False(t, caps.NoSendCap, "a zero cap is a cap, never \"no cap\"")
			assert.Equal(t, int64(0), caps.MaxAmount)

			requireSendCapRefusal(t, svc, wallet)
		})
	}
}

// A cap under the fee reserve plus 1 loki (once the join floor) is accepted:
// the wallet pays only on this node.
func TestHandleCreateCircleWalletEvent_BelowFeeReserve_Accepted(t *testing.T) {
	for _, tc := range []struct{ asked, applied uint64 }{
		{1_000, 1_000}, {1_500, 1_000}, {10_999, 10_000}, {constants.MIN_CIRCLE_WALLET_MAX_MLOKI, constants.MIN_CIRCLE_WALLET_MAX_MLOKI},
	} {
		t.Run(fmt.Sprintf("max_amount_%d", tc.asked), func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			provider := createCircleHubWithCaps(t, svc, 3600, 10_000_000, 1_000_000_000, "")

			reported, wallet := joinResolved(t, svc, provider, tc.asked)
			assert.Equal(t, tc.applied, reported)
			require.NotNil(t, wallet.CircleMaxAmountMloki)
			assert.Equal(t, int64(tc.applied), *wallet.CircleMaxAmountMloki) //nolint:gosec // small

			var perm db.AppPermission
			require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", wallet.ID, constants.PAY_INVOICE_SCOPE).First(&perm).Error)
			assert.Equal(t, int(tc.applied/1000), perm.MaxAmountLoki) //nolint:gosec // small

			budget, err := NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).circleBudget(&wallet, time.Now())
			require.NoError(t, err)
			assert.Equal(t, tc.applied, budget.TotalBudget, "get_budget reports the exact cap")

			requireSendCapRefusal(t, svc, wallet)
		})
	}
}
