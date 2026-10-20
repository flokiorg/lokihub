package api

// Admin paths into a circle wallet: a manual transfer can't move its balance
// (only its hub and its member do), and the generic Disconnect reclaims the
// balance to the hub before deleting it.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
)

func TestTransfer_CircleWallet_RefusedBothWays(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 300_000, 3600)
	wallet := createCircleChild(t, svc, hub, "member")
	tests.FundApp(svc, hub.ID, 500_000, "transfer-hub-fund")
	tests.FundApp(svc, wallet.ID, 50_000, "transfer-wallet-fund")

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.Transfer(context.TODO(), &hub.ID, &wallet.ID, 100_000)
	assert.ErrorIs(t, err, constants.ErrInvalidParams, "into a circle wallet, past its receive cap")
	err = theAPI.Transfer(context.TODO(), &wallet.ID, &hub.ID, 10_000)
	assert.ErrorIs(t, err, constants.ErrInvalidParams, "out of a member's wallet")

	assert.Equal(t, int64(50_000), queries.GetIsolatedBalance(svc.DB, wallet.ID))
	assert.Equal(t, int64(500_000), queries.GetIsolatedBalance(svc.DB, hub.ID))
}

func TestDeleteApp_CircleWallet_ReclaimsToHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	svc.LNClient.(*tests.MockLn).Pubkey = selfPaymentPubkey

	hub := newCircleHub(t, svc, 300_000, 3600)
	wallet, _, err := svc.AppsService.CreateApp("member", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &hub.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)
	tests.FundApp(svc, wallet.ID, 200_000, "deleteapp-fund")

	require.NoError(t, newTestAPIWithService(t, svc).DeleteApp(wallet))

	assert.Nil(t, svc.AppsService.GetAppById(wallet.ID))
	assert.Greater(t, queries.GetIsolatedBalance(svc.DB, hub.ID), int64(0), "the balance went back to the hub, not away with the wallet")
}
