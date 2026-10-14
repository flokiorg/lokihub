package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// NIP-CW §Creating a Circle Wallet, steps 1-6: the rate limit comes after the
// proof, membership and one-wallet checks, so quota is spent only by requests
// that got that far, and a forged or unauthorized request cannot exhaust a
// member's allowance (CW-creating-a-circle-wallet-09/-14). Production keeps one
// limiter for the service and builds a controller per request, so these tests
// do the same. The test service allows 3 joins per hour.

func joinThroughSharedLimiter(t *testing.T, svc *tests.TestService, limiter RateLimiter, cache NostrSocialCache, provider *db.App, requestJSON string) *models.Response {
	t.Helper()
	req := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(requestJSON), req))
	ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(ev).Error)
	controller := NewNip47Controller(svc.LNClient, svc.DB, svc.EventPublisher,
		permissions.NewPermissionsService(svc.DB, svc.EventPublisher), transactions.NewTransactionsService(svc.DB, svc.EventPublisher),
		svc.AppsService, svc.Keys, cache, NewRateLimiter(), NewRateLimiter(), limiter, svc.Cfg, apps.NewIdentityAuthorityManager(svc.DB))
	var response *models.Response
	controller.HandleCreateCircleWalletEvent(context.TODO(), req, ev.ID, provider, func(r *models.Response, _ nostr.Tags) {
		response = r
	})
	require.NotNil(t, response)
	return response
}

func TestCreateCircleWallet_RejectedRequestsDoNotSpendTheMembersQuota(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)
	limiter := NewRateLimiter()
	member := &mockSocialCache{authorized: true}

	victimPriv := nostr.GeneratePrivateKey()
	victim, _ := nostr.GetPublicKey(victimPriv)
	attackerPriv := nostr.GeneratePrivateKey()

	for i := 0; i < 5; i++ {
		// Names the victim, but the proof is the attacker's.
		forged := buildCircleWalletIdentityEvent(t, attackerPriv, *provider.WalletPubkey)
		response := joinThroughSharedLimiter(t, svc, limiter, member, provider,
			rawCircleWalletRequest(t, victim, 100_000, 3600, mustMarshal(t, forged)))
		require.NotNil(t, response.Error)
		require.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
	}
	for i := 0; i < 5; i++ {
		// The victim's own proof, while not (yet) a member.
		response := joinThroughSharedLimiter(t, svc, limiter, &mockSocialCache{authorized: false}, provider,
			distinctCircleWalletRequest(t, victimPriv, *provider.WalletPubkey, fmt.Sprintf("nm-%d", i), 100_000, 3600))
		require.NotNil(t, response.Error)
		require.Equal(t, constants.ERROR_RESTRICTED, response.Error.Code)
	}

	response := joinThroughSharedLimiter(t, svc, limiter, member, provider,
		distinctCircleWalletRequest(t, victimPriv, *provider.WalletPubkey, "real", 100_000, 3600))
	require.Nil(t, response.Error, "ten refused requests must not have used up the member's 3 joins: %+v", response.Error)
}

// The control for the test above: requests that pass every earlier check do
// spend quota, so the limiter is really in the path.
func TestCreateCircleWallet_QuotaSpentByRequestsPastTheChecks(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 0) // nothing to commit, so every join fails after the limiter
	limiter := NewRateLimiter()
	key := nostr.GeneratePrivateKey()

	codes := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		response := joinThroughSharedLimiter(t, svc, limiter, &mockSocialCache{authorized: true}, provider,
			distinctCircleWalletRequest(t, key, *provider.WalletPubkey, fmt.Sprintf("q-%d", i), 100_000, 3600))
		require.NotNil(t, response.Error)
		codes = append(codes, response.Error.Code)
	}
	assert.Equal(t, []string{constants.ERROR_QUOTA_EXCEEDED, constants.ERROR_QUOTA_EXCEEDED, constants.ERROR_QUOTA_EXCEEDED, constants.ERROR_RATE_LIMITED}, codes)
}

// CW-creating-a-circle-wallet-12: while membership can't be decided the Hub
// answers with a distinct, retryable NOT_READY, never a refusal.
func TestCreateCircleWallet_WarmingUp_IsNotReadyNotRestricted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)

	response := joinThroughSharedLimiter(t, svc, NewRateLimiter(), &mockSocialCache{err: constants.ErrSocialCacheWarmingUp}, provider,
		makeCircleWalletRequest(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, 100_000, 3600))
	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_NOT_READY, response.Error.Code)
}
