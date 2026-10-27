package controllers

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CW §Request and §Identity Proof: the member generates the
// wallet's connection key and sends only connection_pubkey, which the identity
// proof binds with a connection tag. These are the refusals, each checked to
// leave nothing behind and, where the spec orders it, to happen before
// membership is consulted.

type countingMembership struct{ calls atomic.Int32 }

func (c *countingMembership) IsAuthorized(context.Context, string, *db.CircleIdentity, *gorm.DB) (bool, error) {
	c.calls.Add(1)
	return true, nil
}

// joinWithConnection sends a join whose request carries connectionPubkey and
// whose proof carries the given connection tags.
func joinWithConnection(t *testing.T, svc *tests.TestService, provider *db.App, cache NostrSocialCache, member, connectionPubkey string, proofConnectionTags ...string) *models.Response {
	t.Helper()
	memberPub, _ := nostr.GetPublicKey(member)
	tags := nostr.Tags{{"d", *provider.WalletPubkey}}
	for _, value := range proofConnectionTags {
		tags = append(tags, nostr.Tag{"connection", value})
	}
	proof := &nostr.Event{Kind: nostrKindCircleIdentityProof, CreatedAt: nostr.Now(), Tags: tags}
	require.NoError(t, proof.Sign(member))
	joinProofSigners.Store(proof.ID, member)

	raw, err := json.Marshal(map[string]interface{}{"method": constants.NIP47MethodCreateCircleWallet, "params": nipcw.CreateCircleWalletRequest{
		Pubkey: memberPub, ConnectionPubkey: connectionPubkey, MaxAmount: 100_000, Expiry: 3600, IdentityEvent: mustMarshal(t, proof),
	}})
	require.NoError(t, err)
	req := &models.Request{}
	require.NoError(t, json.Unmarshal(raw, req))
	ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(ev).Error)

	var response *models.Response
	NewTestNip47ControllerWithSocialCache(svc, cache).testJoin(t, context.TODO(), req, ev.ID, provider,
		func(r *models.Response, _ nostr.Tags) { response = r })
	require.NotNil(t, response)
	return response
}

func requireNoCircleWallet(t *testing.T, svc *tests.TestService, provider *db.App) {
	t.Helper()
	var wallets, memberships int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Count(&wallets).Error)
	require.NoError(t, svc.DB.Model(&db.CircleWalletMembership{}).Where("circle_hub_app_id = ?", provider.ID).Count(&memberships).Error)
	assert.Zero(t, wallets, "a refused join creates no wallet")
	assert.Zero(t, memberships, "a refused join records no membership")
}

func TestCreateCircleWallet_ConnectionPubkey_Refusals(t *testing.T) {
	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	valid, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	for name, connection := range map[string]string{
		"missing":            "",
		"uppercase":          strings.ToUpper(valid),
		"too short":          valid[:63],
		"not hex":            "zz" + valid[2:],
		"not on the curve":   strings.Repeat("ff", 32),
		"same as the pubkey": memberPub,
		"npub, not hex":      "npub1" + valid[:58],
	} {
		t.Run(name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			provider := createCircleHub(t, svc, 7200, 1_000_000)
			cache := &countingMembership{}

			response := joinWithConnection(t, svc, provider, cache, member, connection, connection)
			require.NotNil(t, response.Error)
			assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
			assert.Contains(t, response.Error.Message, "connection_pubkey")
			assert.Zero(t, cache.calls.Load(), "refused before membership is consulted")
			requireNoCircleWallet(t, svc, provider)
		})
	}
}

// Identity Proof check 6 runs with checks 1-5, before membership.
func TestCreateCircleWallet_ProofConnectionTag_CheckedBeforeMembership(t *testing.T) {
	member := nostr.GeneratePrivateKey()
	connection, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	attacker, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	for name, tags := range map[string][]string{
		"missing":                  nil,
		"another key (re-pointed)": {attacker},
		"two tags":                 {connection, attacker},
	} {
		t.Run(name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			provider := createCircleHub(t, svc, 7200, 1_000_000)
			cache := &countingMembership{}

			response := joinWithConnection(t, svc, provider, cache, member, connection, tags...)
			require.NotNil(t, response.Error)
			assert.Equal(t, constants.ERROR_UNAUTHORIZED, response.Error.Code)
			assert.Contains(t, response.Error.Message, "connection")
			assert.Zero(t, cache.calls.Load(), "refused before membership is consulted")
			requireNoCircleWallet(t, svc, provider)
		})
	}
}

// A connection_pubkey already authorized for another connection on this node is
// refused, and the whole transaction rolls back.
func TestCreateCircleWallet_ConnectionPubkeyAlreadyAuthorized_RolledBack(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)

	taken, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	_, _, err = svc.AppsService.CreateApp("someone else's app", taken, 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindStandard, nil, "", nil)
	require.NoError(t, err)

	response := joinWithConnection(t, svc, provider, &countingMembership{}, nostr.GeneratePrivateKey(), taken, taken)
	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, response.Error.Code)
	assert.Contains(t, response.Error.Message, "connection_pubkey")
	requireNoCircleWallet(t, svc, provider)

	var holders int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("app_pubkey = ?", taken).Count(&holders).Error)
	assert.EqualValues(t, 1, holders, "the existing connection keeps the key; nothing else gets it")
}
