package nip47

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/controllers"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// listedCircle is a funded circle hub and a listing connection of it, as the
// listing service creates one: a circle_hub_conn child sharing the hub's wallet
// pubkey, with its own client key derived from its id.
func listedCircle(t *testing.T, svc *tests.TestService) (hub *db.App, hubSecret string, conn *db.App, connSecret string) {
	t.Helper()
	hub, hubSecret, err := svc.AppsService.CreateCircleHub("circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_INFO_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 7200, PerWalletMaxMloki: 1_000_000})
	require.NoError(t, err)
	tests.FundApp(svc, hub.ID, 1_000_000, tests.RandomHex32())
	parent := hub.ID
	w := *hub.WalletPubkey
	conn = &db.App{Name: "Listing connection", Kind: db.AppKindCircleHubConn, ParentAppID: &parent,
		ParentKind: db.ParentKindListing, WalletPubkey: &w}
	require.NoError(t, svc.DB.Create(conn).Error)
	connSecret, err = svc.Keys.GetCashPairingKey(conn.ID)
	require.NoError(t, err)
	conn.AppPubkey, err = nostr.GetPublicKey(connSecret)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(conn).Update("app_pubkey", conn.AppPubkey).Error)
	return hub, hubSecret, conn, connSecret
}

// sendStandard sends payload to walletPubkey from clientSecret with encryption
// enc and returns the reply event and its decrypted response.
func sendStandard(t *testing.T, svc *tests.TestService, nip47svc *nip47Service, walletPubkey, clientSecret, enc string, payload any) (*nostr.Event, models.Response) {
	t.Helper()
	clientPubkey, err := nostr.GetPublicKey(clientSecret)
	require.NoError(t, err)
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	c, err := cipher.NewNip47Cipher(enc, walletPubkey, clientSecret)
	require.NoError(t, err)
	content, err := c.Encrypt(string(raw))
	require.NoError(t, err)
	tags := nostr.Tags{{"p", walletPubkey}}
	if enc != constants.ENCRYPTION_TYPE_NIP04 {
		tags = append(tags, nostr.Tag{"encryption", enc})
	}
	request := &nostr.Event{Kind: models.REQUEST_KIND, PubKey: clientPubkey, CreatedAt: nostr.Now(), Tags: tags, Content: content}
	require.NoError(t, request.Sign(clientSecret))
	pool := tests.NewMockSimplePool()
	nip47svc.HandleEvent(context.TODO(), pool, request, svc.LNClient)
	require.NotEmpty(t, pool.PublishedEvents)
	reply := pool.PublishedEvents[0]
	assert.Equal(t, request.ID, reply.Tags.Find("e")[1])
	plain, err := c.Decrypt(reply.Content)
	require.NoError(t, err)
	var response models.Response
	require.NoError(t, json.Unmarshal([]byte(plain), &response))
	return reply, response
}

// U-25.12 and U-25.RC2: a listing connection's get_info, NIP-44 and NIP-04,
// decrypts under TLV 0 and is answered by TLV 0 with the hub's circle block,
// methods ["get_info"]; every other method is NOT_IMPLEMENTED.
func TestListingConn_GetInfoAsHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, everyoneIsAMember{})
	hub, _, conn, connSecret := listedCircle(t, svc)

	for _, enc := range []string{constants.ENCRYPTION_TYPE_NIP44_V2, constants.ENCRYPTION_TYPE_NIP04} {
		controllers.InvalidateListedGetInfo(conn.ID)
		reply, resp := sendStandard(t, svc, nip47svc, *hub.WalletPubkey, connSecret, enc,
			map[string]any{"method": models.GET_INFO_METHOD, "params": map[string]any{}})
		assert.Equal(t, *hub.WalletPubkey, reply.PubKey, "authored by TLV 0 (%s)", enc)
		require.Nil(t, resp.Error, enc)
		var result map[string]any
		b, _ := json.Marshal(resp.Result)
		require.NoError(t, json.Unmarshal(b, &result))
		assert.Equal(t, []any{"get_info"}, result["methods"])
		assert.ElementsMatch(t, []any{constants.NIP47MethodCreateCircleWallet, constants.NIP47MethodCashLookup}, result["private_methods"])
		assert.NotNil(t, result["circle_wallet"])
		assert.Nil(t, result["metadata"], "the hub's admin metadata is not shown")
		assert.Equal(t, "flokicoin", result["chain"])
	}

	_, resp := sendStandard(t, svc, nip47svc, *hub.WalletPubkey, connSecret, constants.ENCRYPTION_TYPE_NIP44_V2,
		map[string]any{"method": models.GET_BALANCE_METHOD, "params": map[string]any{}})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_NOT_IMPLEMENTED, resp.Error.Code)
}

// U-25.10: a listed connection's get_info is never limited and is answered from
// a cache of at most 5 s, dropped on invalidation.
func TestListingConn_GetInfoCache(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, everyoneIsAMember{})
	hub, _, conn, connSecret := listedCircle(t, svc)
	controllers.InvalidateListedGetInfo(conn.ID)

	accepting := func() any {
		_, resp := sendStandard(t, svc, nip47svc, *hub.WalletPubkey, connSecret, constants.ENCRYPTION_TYPE_NIP44_V2,
			map[string]any{"method": models.GET_INFO_METHOD, "params": map[string]any{}})
		require.Nil(t, resp.Error)
		var result struct {
			CircleWallet map[string]any `json:"circle_wallet"`
		}
		b, _ := json.Marshal(resp.Result)
		require.NoError(t, json.Unmarshal(b, &result))
		return result.CircleWallet["accepting"]
	}
	for i := 0; i < 200; i++ {
		require.Equal(t, true, accepting())
	}
	no := false
	require.NoError(t, svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", hub.ID).Update("accepting_joins", &no).Error)
	assert.Equal(t, true, accepting(), "cached")
	controllers.InvalidateListedGetInfo(conn.ID)
	assert.Equal(t, false, accepting(), "invalidated: the closed signal is never stale")
}

// H-CW-072 and the join record (WP-14): a join whose bill proof is signed by a
// live listing connection's key is served by the hub and records that
// connection; once the listing connection is deleted (revoked), a join through
// it is omitted.
func TestListingConn_PrivateJoin(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	require.NoError(t, svc.Cfg.SetUpdate("Relay", "wss://relay.example", ""))
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, everyoneIsAMember{})
	hub, _, conn, connSecret := listedCircle(t, svc)

	join := func() (*nipcw.PreparedJoin, string) {
		member := nostr.GeneratePrivateKey()
		connection, err := nipcw.NewConnectionKey()
		require.NoError(t, err)
		prepared, err := nipcw.CreateCircleWalletParams{Credential: nipcw.BySigning(member), Connection: connection,
			MaxAmountMillis: 100_000, Expiry: time.Hour}.Prepare(*hub.WalletPubkey, time.Now())
		require.NoError(t, err)
		return prepared, member
	}
	prepared, member := join()
	raw := privateJoin(t, svc, nip47svc, hub, connSecret, member, prepared.Request)
	_, err = prepared.ParseResult(raw)
	require.NoError(t, err)
	var rec db.CircleJoinRecord
	require.NoError(t, svc.DB.Where("circle_hub_app_id = ?", hub.ID).First(&rec).Error)
	assert.Equal(t, conn.AppPubkey, rec.HubConnPubkey, "the record names the connection used")

	// Revoked: the row is gone and the proof verifies against no live connection.
	require.NoError(t, svc.DB.Delete(&db.App{}, conn.ID).Error)
	prepared, member = join()
	params, err := json.Marshal(prepared.Request)
	require.NoError(t, err)
	hubXOnly := strings.Repeat("ab", 32)
	nonce := tests.RandomHex32()
	notAfter := time.Now().Add(time.Minute).Unix()
	item := buildItem(t, "join", *hub.WalletPubkey, constants.NIP47MethodCreateCircleWallet, string(params),
		member, connSecret, hubXOnly, nonce, notAfter)
	_, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
		PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
	assert.False(t, served, "omitted")
}

func TestWalletKeyAppID(t *testing.T) {
	parent := uint(7)
	assert.Equal(t, uint(7), walletKeyAppID(&db.App{ID: 9, Kind: db.AppKindCircleHubConn, ParentAppID: &parent}))
	assert.Equal(t, uint(9), walletKeyAppID(&db.App{ID: 9, Kind: db.AppKindCircleWallet, ParentAppID: &parent}))
}
