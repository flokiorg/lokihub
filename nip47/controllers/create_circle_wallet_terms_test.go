package controllers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// joinWithTerms joins provider with the given max_receive and expiry (0 =
// omitted), the proof's terms tag signed over them.
func joinWithTerms(t *testing.T, svc *tests.TestService, provider *db.App, member string, maxReceive uint64, expiry int) *models.Response {
	t.Helper()
	var content struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(distinctCircleWalletRequest(t, member, *provider.WalletPubkey, tests.RandomHex32(), 100_000, expiry)), &content))
	content.Params["max_receive"] = maxReceive
	if expiry == 0 {
		delete(content.Params, "expiry")
	}
	raw, err := json.Marshal(content.Params)
	require.NoError(t, err)
	dbRequestEvent := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(&dbRequestEvent).Error)

	var published *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		testJoin(t, context.TODO(), &models.Request{Method: content.Method, Params: raw}, dbRequestEvent.ID, provider,
			func(r *models.Response, _ nostr.Tags) { published = r })
	require.NotNil(t, published)
	return published
}

// A max_receive over the Hub's per-wallet receive ceiling is LIMIT_EXCEEDED
// naming the field (NIP-CW §Processing Algorithm step 6); at the ceiling it is
// applied, stored and reported as asked.
func TestCreateCircleWallet_MaxReceiveCeiling(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)
	require.NoError(t, svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", provider.ID).
		Update("per_wallet_max_receive_mloki", 250_000).Error)

	over := joinWithTerms(t, svc, provider, nostr.GeneratePrivateKey(), 250_001, 3600)
	require.NotNil(t, over.Error)
	assert.Equal(t, nipcw.ErrLimitExceeded, over.Error.Code)
	assert.Equal(t, "max_receive exceeds the Hub's limit", over.Error.Message)

	member := nostr.GeneratePrivateKey()
	ok := joinWithTerms(t, svc, provider, member, 250_000, 3600)
	require.Nil(t, ok.Error, "%+v", ok.Error)
	details := decryptCircleWalletDetails(t, member, *provider.WalletPubkey, ok.Result.(createCircleWalletResponse).EncryptedDetails)
	assert.Equal(t, uint64(250_000), details.MaxReceive)

	var wallet db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", details.WalletPubkey).First(&wallet).Error)
	require.NotNil(t, wallet.CircleMaxReceiveMloki)
	assert.Equal(t, int64(250_000), *wallet.CircleMaxReceiveMloki)
}

// An omitted expiry takes the Hub's default lifetime, not its ceiling (NIP-CW
// §Request).
func TestCreateCircleWallet_OmittedExpiryTakesTheDefault(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)
	require.NoError(t, svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", provider.ID).
		Update("default_exp_secs", 600).Error)

	member := nostr.GeneratePrivateKey()
	before := time.Now().Unix()
	response := joinWithTerms(t, svc, provider, member, 100_000, 0)
	require.Nil(t, response.Error, "%+v", response.Error)
	details := decryptCircleWalletDetails(t, member, *provider.WalletPubkey, response.Result.(createCircleWalletResponse).EncryptedDetails)
	assert.GreaterOrEqual(t, details.ExpiresAt, before+600)
	assert.LessOrEqual(t, details.ExpiresAt, time.Now().Unix()+600)
}

// get_info reports the receive ceiling beside the send ceiling.
func TestGetInfo_CircleHubReportsReceiveCeiling(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)
	require.NoError(t, svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", provider.ID).
		Update("per_wallet_max_receive_mloki", 333_000).Error)

	terms, err := NewTestNip47Controller(svc).circleWalletTerms(provider, "")
	require.NoError(t, err)
	assert.Equal(t, int64(333_000), terms.PerWalletMaxReceiveMillis)
}
