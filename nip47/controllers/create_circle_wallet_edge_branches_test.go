package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// sendJoin sends one raw create_circle_wallet request to provider.
func sendJoin(t *testing.T, svc *tests.TestService, provider *db.App, cache NostrSocialCache, request string) *models.Response {
	t.Helper()
	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(request), nip47Request))
	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)
	var published *models.Response
	NewTestNip47ControllerWithSocialCache(svc, cache).
		HandleCreateCircleWalletEvent(context.TODO(), nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			published = r
		})
	require.NotNil(t, published)
	return published
}

func requireNoCircleWallets(t *testing.T, svc *tests.TestService, provider *db.App) {
	t.Helper()
	var count int64
	require.NoError(t, svc.DB.Model(&db.App{}).
		Where("parent_app_id = ? AND parent_kind = ?", provider.ID, db.ParentKindCircle).Count(&count).Error)
	assert.Zero(t, count)
}

// NIP-CW §Response: the member reaches the wallet only through the relays the
// hub sends back, so a hub with none issues nothing.
func TestHandleCreateCircleWalletEvent_NoRelay_IssuesNothing(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 3600, 1_000_000)
	require.NoError(t, svc.Cfg.SetUpdate("Relay", "", ""))

	resp := sendJoin(t, svc, provider, &mockSocialCache{authorized: true},
		makeCircleWalletRequest(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, 20_000, 3600))
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_INTERNAL, resp.Error.Code)
	requireNoCircleWallets(t, svc, provider)
}

// A hub without its wallet pubkey can't check what a proof is bound to, so it
// issues nothing rather than trusting the proof.
func TestHandleCreateCircleWalletEvent_HubWithoutWalletPubkey_IssuesNothing(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 3600, 1_000_000)
	request := makeCircleWalletRequest(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, 20_000, 3600)
	provider.WalletPubkey = nil

	resp := sendJoin(t, svc, provider, &mockSocialCache{authorized: true}, request)
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_INTERNAL, resp.Error.Code)
	requireNoCircleWallets(t, svc, provider)
}

// Known limit, pinned: the proof is recorded before membership is checked, so
// resending the exact same request after a NOT_READY (same proof, same
// second) reads as a replay. nmilat signs a fresh proof per attempt, so a
// client retry made a second later is unaffected.
func TestHandleCreateCircleWalletEvent_IdenticalRetryAfterNotReady_ReadsAsReplay(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 3600, 1_000_000)
	request := makeCircleWalletRequest(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, 20_000, 3600)

	first := sendJoin(t, svc, provider, &mockSocialCache{err: constants.ErrSocialCacheWarmingUp}, request)
	require.NotNil(t, first.Error)
	require.Equal(t, constants.ERROR_NOT_READY, first.Error.Code)

	retry := sendJoin(t, svc, provider, &mockSocialCache{authorized: true}, request)
	require.NotNil(t, retry.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, retry.Error.Code)
	assert.Contains(t, retry.Error.Message, "already been used")
}
