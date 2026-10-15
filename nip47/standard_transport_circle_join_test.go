package nip47

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

type everyoneIsAMember struct{}

func (everyoneIsAMember) IsAuthorized(context.Context, string, *db.CircleIdentity, *gorm.DB) (bool, error) {
	return true, nil
}

// NIP-CW §Private Join: a Hub that offers the private join MUST keep accepting
// the standard transport, since a prospective member may hold nothing but the
// Hub connection string and an un-updated client (CW-private-join-02).
func TestStandardTransport_CircleJoinIsStillServed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, everyoneIsAMember{})

	hub, pairingSecret, err := svc.AppsService.CreateCircleHub("circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 7200, PerWalletMaxMloki: 1_000_000})
	require.NoError(t, err)
	tests.FundApp(svc, hub.ID, 1_000_000, tests.RandomHex32())
	connPubkey, err := nostr.GetPublicKey(pairingSecret)
	require.NoError(t, err)

	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	proof := &nostr.Event{Kind: nipcw.KindCircleIdentityProof, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", *hub.WalletPubkey}}}
	require.NoError(t, proof.Sign(member))
	proofJSON, err := json.Marshal(proof)
	require.NoError(t, err)

	payload, err := json.Marshal(map[string]interface{}{
		"method": constants.NIP47MethodCreateCircleWallet,
		"params": nipcw.CreateCircleWalletRequest{Pubkey: memberPub, MaxAmount: 100_000, Expiry: 3600, IdentityEvent: string(proofJSON)},
	})
	require.NoError(t, err)
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *hub.WalletPubkey, pairingSecret)
	require.NoError(t, err)
	content, err := c.Encrypt(string(payload))
	require.NoError(t, err)
	request := &nostr.Event{
		Kind: models.REQUEST_KIND, PubKey: connPubkey, CreatedAt: nostr.Now(),
		Tags: nostr.Tags{{"encryption", constants.ENCRYPTION_TYPE_NIP44_V2}}, Content: content,
	}
	require.NoError(t, request.Sign(pairingSecret))

	pool := tests.NewMockSimplePool()
	nip47svc.HandleEvent(context.TODO(), pool, request, svc.LNClient)
	require.NotEmpty(t, pool.PublishedEvents)
	plain, err := c.Decrypt(pool.PublishedEvents[0].Content)
	require.NoError(t, err)
	var response models.Response
	require.NoError(t, json.Unmarshal([]byte(plain), &response))
	require.Nil(t, response.Error, "%+v", response.Error)
	require.Equal(t, constants.NIP47MethodCreateCircleWallet, response.ResultType)
}
