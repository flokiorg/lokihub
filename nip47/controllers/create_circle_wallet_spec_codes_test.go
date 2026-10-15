package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CW §Errors and the join response (SD-17, SD-20).

func joinCircleWith(t *testing.T, svc *tests.TestService, provider *db.App, cache NostrSocialCache, requesterKey string, maxAmountMloki uint64) *models.Response {
	t.Helper()
	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal(
		[]byte(distinctCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, tests.RandomHex32(), maxAmountMloki, 3600)), nip47Request))
	dbRequestEvent := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(&dbRequestEvent).Error)

	var published *models.Response
	NewTestNip47ControllerWithSocialCache(svc, cache).
		HandleCreateCircleWalletEvent(context.TODO(), nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			published = r
		})
	require.NotNil(t, published)
	return published
}

// Each refusal is an error response with its own code, so a client can tell
// "retry later" from "not a member" from "you already have one".
func TestCreateCircleWallet_RefusalsAreErrorsAndDistinct(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)

	notReady := joinCircleWith(t, svc, provider, &mockSocialCache{err: constants.ErrSocialCacheWarmingUp}, nostr.GeneratePrivateKey(), 100_000)
	notMember := joinCircleWith(t, svc, provider, &mockSocialCache{authorized: false}, nostr.GeneratePrivateKey(), 100_000)
	member := nostr.GeneratePrivateKey()
	first := joinCircleWith(t, svc, provider, &mockSocialCache{authorized: true}, member, 100_000)
	require.Nil(t, first.Error, "%+v", first.Error)
	again := joinCircleWith(t, svc, provider, &mockSocialCache{authorized: true}, member, 100_000)

	codes := map[string]*models.Response{
		nipcw.ErrNotReady:     notReady,
		nipcw.ErrRestricted:   notMember,
		nipcw.ErrWalletExists: again,
	}
	for want, response := range codes {
		require.NotNil(t, response.Error, "%s must be an error response", want)
		assert.Nil(t, response.Result, "%s must carry no result", want)
		assert.Equal(t, want, response.Error.Code)
	}
}

// NOT_READY whenever membership can't be decided, not only during warm-up: an
// unexpected failure checking membership is not a refusal either.
func TestCreateCircleWallet_MembershipCheckFailure_IsNotReady(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)

	response := joinCircleWith(t, svc, provider, &mockSocialCache{err: errors.New("cache store unavailable")}, nostr.GeneratePrivateKey(), 100_000)
	require.NotNil(t, response.Error)
	assert.Equal(t, nipcw.ErrNotReady, response.Error.Code)
}

// The resolved max_amount travels inside encrypted_details and nowhere else, and
// nmilat's own client reads it back. The wallet's cap is stored in whole loki,
// so a 100,500 mloki request resolves to 100,000.
func TestCreateCircleWallet_ResolvedMaxAmountInEncryptedDetails(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)

	member := nostr.GeneratePrivateKey()
	response := joinCircleWith(t, svc, provider, &mockSocialCache{authorized: true}, member, 100_500)
	require.Nil(t, response.Error, "%+v", response.Error)

	raw, err := json.Marshal(response.Result)
	require.NoError(t, err)
	var outer map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &outer))
	assert.Len(t, outer, 1, "only encrypted_details is outside the encryption")
	assert.NotContains(t, outer, "max_amount")

	details := decryptCircleWalletDetails(t, member, *provider.WalletPubkey,
		response.Result.(createCircleWalletResponse).EncryptedDetails)
	assert.Equal(t, uint64(100_000), details.MaxAmount)

	memberPub, _ := nostr.GetPublicKey(member)
	connection, err := nipcw.ConnectionKeyFromSecret(testConnectionSecret(memberPub))
	require.NoError(t, err)
	parsed, err := nipcw.CreateCircleWalletParams{Credential: nipcw.BySigning(member), Connection: connection, MaxAmountMillis: 100_500}.
		ParseResult(*provider.WalletPubkey, raw)
	require.NoError(t, err)
	require.NotNil(t, parsed.MaxAmountMillis)
	assert.Equal(t, uint64(100_000), *parsed.MaxAmountMillis)
}
