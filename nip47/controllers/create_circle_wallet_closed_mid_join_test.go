package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// closingSocialCache authorizes the member, and runs close while doing so: the
// Hub changes between the join's early accepting check and its commit.
type closingSocialCache struct{ close func() }

func (c *closingSocialCache) IsAuthorized(_ context.Context, _ string, _ *db.CircleIdentity, _ *gorm.DB) (bool, error) {
	c.close()
	return true, nil
}

// A join whose Hub stopped accepting, or was torn down, after the join's early
// check is refused at its commit, and no wallet is created (NIP-CW §The Circle
// Wallet Hub Connection, §Lifecycle and Deletion).
func TestCreateCircleWallet_HubClosedMidJoinIsRefused(t *testing.T) {
	for name, update := range map[string]map[string]interface{}{
		"accepting off": {"accepting_joins": false},
		"torn down":     {"teardown_requested_at": int64(1)},
	} {
		t.Run(name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			provider := createCircleHub(t, svc, 7200, 10_000_000)
			cache := &closingSocialCache{close: func() {
				require.NoError(t, svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", provider.ID).Updates(update).Error)
			}}

			nip47Request := &models.Request{}
			require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, 100_000, 3600)), nip47Request))
			ev := &db.RequestEvent{}
			svc.DB.Create(&ev)
			var resp *models.Response
			NewTestNip47ControllerWithSocialCache(svc, cache).
				testJoin(t, context.TODO(), nip47Request, ev.ID, provider, func(r *models.Response, _ nostr.Tags) { resp = r })

			require.NotNil(t, resp)
			require.NotNil(t, resp.Error)
			assert.Equal(t, constants.ERROR_RESTRICTED, resp.Error.Code)
			var wallets int64
			require.NoError(t, svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Count(&wallets).Error)
			assert.Zero(t, wallets)
		})
	}
}
