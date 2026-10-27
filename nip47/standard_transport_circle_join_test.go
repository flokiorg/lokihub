package nip47

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

type everyoneIsAMember struct{}

func (everyoneIsAMember) IsAuthorized(context.Context, string, *db.CircleIdentity, *gorm.DB) (bool, error) {
	return true, nil
}

// sendOverStandardTransport encrypts payload to walletPubkey with clientSecret,
// sends it as a kind-23194 request signed by clientSecret, and returns the
// decrypted reply.
func sendOverStandardTransport(t *testing.T, svc *tests.TestService, nip47svc *nip47Service, walletPubkey, clientSecret string, payload interface{}) models.Response {
	t.Helper()
	clientPubkey, err := nostr.GetPublicKey(clientSecret)
	require.NoError(t, err)
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, walletPubkey, clientSecret)
	require.NoError(t, err)
	content, err := c.Encrypt(string(raw))
	require.NoError(t, err)
	request := &nostr.Event{
		Kind: models.REQUEST_KIND, PubKey: clientPubkey, CreatedAt: nostr.Now(),
		Tags: nostr.Tags{{"encryption", constants.ENCRYPTION_TYPE_NIP44_V2}, {"p", walletPubkey}}, Content: content,
	}
	require.NoError(t, request.Sign(clientSecret))

	pool := tests.NewMockSimplePool()
	nip47svc.HandleEvent(context.TODO(), pool, request, svc.LNClient)
	require.NotEmpty(t, pool.PublishedEvents)
	plain, err := c.Decrypt(pool.PublishedEvents[0].Content)
	require.NoError(t, err)
	var response models.Response
	require.NoError(t, json.Unmarshal([]byte(plain), &response))
	return response
}

// NIP-CW §Creating a Circle Wallet and §Pairing Connection, end to end over the
// standard transport, which a Hub offering the private join MUST keep serving
// (CW-private-join-02):
//
//   - nmilat's client generates the connection key and builds the join;
//   - this Hub issues the wallet to that key and returns no secret;
//   - nmilat's client turns the reply into a connection string;
//   - the member then reaches the wallet with the secret only it ever held.
//
// The member's secret must appear nowhere on the Hub: not in the reply, not in
// its database, not in its logs.
func TestStandardTransport_CircleJoin_MemberKeyEndToEnd(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	require.NoError(t, svc.Cfg.SetUpdate("Relay", "wss://relay.example", ""))
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, everyoneIsAMember{})

	var logs bytes.Buffer
	saved := logger.Logger
	logger.Logger = zerolog.New(&logs).Level(zerolog.TraceLevel)
	t.Cleanup(func() { logger.Logger = saved })

	hub, hubSecret, err := svc.AppsService.CreateCircleHub("circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 7200, PerWalletMaxMloki: 1_000_000})
	require.NoError(t, err)
	tests.FundApp(svc, hub.ID, 1_000_000, tests.RandomHex32())

	member := nostr.GeneratePrivateKey()
	connection, err := nipcw.NewConnectionKey()
	require.NoError(t, err)
	join := nipcw.CreateCircleWalletParams{
		Credential:      nipcw.BySigning(member),
		Connection:      connection,
		MaxAmountMillis: 100_000,
		Expiry:          time.Hour,
	}
	prepared, err := join.Prepare(*hub.WalletPubkey, time.Now())
	require.NoError(t, err)
	params := prepared.Request

	response := sendOverStandardTransport(t, svc, nip47svc, *hub.WalletPubkey, hubSecret,
		map[string]interface{}{"method": constants.NIP47MethodCreateCircleWallet, "params": params})
	require.Nil(t, response.Error, "%+v", response.Error)
	require.Equal(t, constants.NIP47MethodCreateCircleWallet, response.ResultType)

	raw, err := json.Marshal(response.Result)
	require.NoError(t, err)
	issued, err := prepared.ParseResult(raw)
	require.NoError(t, err, "nmilat's client must accept this Hub's reply")
	assert.Equal(t, []string{"wss://relay.example"}, issued.Relays)

	// The reply carries nothing secret, and nothing the member did not already have.
	hubWalletKey, err := svc.Keys.GetAppWalletKey(hub.ID)
	require.NoError(t, err)
	memberPub, _ := nostr.GetPublicKey(member)
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, memberPub, hubWalletKey)
	require.NoError(t, err)
	var outer struct {
		EncryptedDetails string `json:"encrypted_details"`
	}
	require.NoError(t, json.Unmarshal(raw, &outer))
	details, err := c.Decrypt(outer.EncryptedDetails)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(details), &fields))
	for _, forbidden := range []string{"secret", "pairing_uri", "connection_secret"} {
		assert.NotContains(t, fields, forbidden)
	}
	assert.NotContains(t, details, "nostr+walletconnect")

	// The wallet was issued to the member's own key.
	var wallet db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", issued.WalletPubkey).First(&wallet).Error)
	assert.Equal(t, db.AppKindCircleWallet, wallet.Kind)
	assert.Equal(t, connection.Pubkey(), wallet.AppPubkey)

	// The member reaches it with its own connection secret (no URI form is issued).
	require.Equal(t, connection.Pubkey(), issued.ConnectionPubkey)
	require.Empty(t, issued.LeakedFields)
	balance := sendOverStandardTransport(t, svc, nip47svc, issued.WalletPubkey, connection.Secret(),
		map[string]interface{}{"method": models.GET_BALANCE_METHOD})
	require.Nil(t, balance.Error, "the member's own connection key must be authorized: %+v", balance.Error)

	// And the secret exists nowhere on the Hub.
	assert.NotContains(t, logs.String(), connection.Secret(), "the member's secret must never be logged")
	assertNotStoredAnywhere(t, svc.DB, connection.Secret())
}

// assertNotStoredAnywhere scans every column of every table for needle.
func assertNotStoredAnywhere(t *testing.T, gormDB *gorm.DB, needle string) {
	t.Helper()
	if gormDB.Name() != "sqlite" {
		t.Log("table scan reads sqlite_master; skipped on this backend")
		return
	}
	var tables []string
	require.NoError(t, gormDB.Raw("SELECT name FROM sqlite_master WHERE type = 'table'").Scan(&tables).Error)
	require.NotEmpty(t, tables)
	for _, table := range tables {
		rows, err := gormDB.Raw(fmt.Sprintf("SELECT * FROM %q", table)).Rows()
		require.NoError(t, err)
		columns, err := rows.Columns()
		require.NoError(t, err)
		for rows.Next() {
			values := make([]interface{}, len(columns))
			pointers := make([]interface{}, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			require.NoError(t, rows.Scan(pointers...))
			for i, value := range values {
				var text string
				switch v := value.(type) {
				case string:
					text = v
				case []byte:
					text = string(v)
				}
				require.False(t, strings.Contains(strings.ToLower(text), needle), "the secret is stored in %s.%s", table, columns[i])
			}
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
}
