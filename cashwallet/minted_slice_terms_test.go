package cashwallet

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Data Model: each slice records its own floor and redeem fee, and a
// freshly minted slice takes them from the Cash Hub's configured defaults.
func TestCreate_MintedSliceCarriesTheHubsTerms(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")
	floor, feePpm, feeBase := int64(700), 25_000, int64(300)
	require.NoError(t, svc.AppsService.UpdateCashHubConfig(hub.ID, nil, nil, &floor, &feePpm, &feeBase, nil))

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub, Recipients: oneCashRecipient(1000), ExpirySecs: 1800,
	})
	require.NoError(t, err)

	var slice db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", result.WalletApp.ID).First(&slice).Error)
	assert.Equal(t, floor, slice.MinTransferMloki)
	assert.Equal(t, feePpm, slice.RedeemFeePpm)
	assert.Equal(t, feeBase, slice.RedeemFeeBaseMloki)
}
