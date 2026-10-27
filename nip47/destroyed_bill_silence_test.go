package nip47

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// U-11.R2a: a kind-23194 request naming a destroyed bill inside its window gets
// no reply, even from its holder; the same bill's cash_status on the private
// transport gets the tombstone.
func TestDestroyedBill_StandardTransportSilent_PrivateAnswers(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletAppID, walletPubkey := seedDestroyedBill(t, svc, time.Now().Add(-time.Hour), 24*60*60)
	pairingKey, err := svc.Keys.GetCashPairingKey(walletAppID)
	require.NoError(t, err)
	senderPubkey, err := nostr.GetPublicKey(pairingKey)
	require.NoError(t, err)

	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, walletPubkey, pairingKey)
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{"method": nipcash.MethodCashStatus})
	require.NoError(t, err)
	content, err := c.Encrypt(string(payload))
	require.NoError(t, err)
	ev := &nostr.Event{Kind: models.REQUEST_KIND, PubKey: senderPubkey, CreatedAt: nostr.Now(),
		Tags:    nostr.Tags{{"p", walletPubkey}, {"encryption", constants.ENCRYPTION_TYPE_NIP44_V2}},
		Content: content}
	require.NoError(t, ev.Sign(pairingKey))

	pool := tests.NewMockSimplePool()
	nip47svc.HandleEvent(context.TODO(), pool, ev, svc.LNClient)
	assert.Empty(t, pool.PublishedEvents, "kind 23194 answers a destroyed bill with silence")

	hubXOnly := strings.Repeat("ab", 32)
	nonce := tests.RandomHex32()
	notAfter := time.Now().Add(time.Minute).Unix()
	item := buildItem(t, "s", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
		nostr.GeneratePrivateKey(), pairingKey, hubXOnly, nonce, notAfter)
	res, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
		PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.True(t, served)
	assert.Contains(t, string(res.Result), `"status":"spent"`)
}
