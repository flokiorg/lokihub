package controllers

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// U-12.7 (NIP-CASH §Network and Chain, §Connection Signatures): a Cash Hub's
// get_info carries network, chain and its node identity as an x-only pubkey,
// with or without the get_info scope; a bill's carries network and chain.
func TestGetInfo_CashConnectionsCarryNetworkChainAndIdentity(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", hub.ID, constants.GET_INFO_SCOPE).Delete(&db.AppPermission{}).Error)
	bill := newFundedCashWallet(t, svc, hub, 1000)

	for _, app := range []*db.App{hub, bill} {
		var resp *models.Response
		NewTestNip47Controller(svc).HandleGetInfoEvent(context.TODO(), &models.Request{Method: "get_info"}, 0, app,
			func(r *models.Response, _ nostr.Tags) { resp = r })
		require.Nil(t, resp.Error)
		info := resp.Result.(*getInfoResponse)
		require.NotNil(t, info.Network, app.Kind)
		require.NotNil(t, info.Chain, app.Kind)
		assert.Equal(t, "flokicoin", *info.Chain)
		assert.Contains(t, []string{"mainnet", "testnet", "signet", "regtest"}, *info.Network)
		if app.Kind == db.AppKindCashHub {
			require.NotNil(t, info.Pubkey)
			assert.Len(t, *info.Pubkey, 64, "the node identity, x-only")
		}
	}
}
