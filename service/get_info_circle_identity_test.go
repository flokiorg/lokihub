package service

// NIP-CW §The Circle Wallet Hub Connection: get_info reports circle_wallet.identity, the
// key that signs the Hub's kind-11190 announcement, only while the private transport runs.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

type circleIdentityHub struct {
	svc          *service
	ln           *announcingLN
	walletPubkey string
	clientSecret string
}

// newCircleIdentityHub wires the nip47 service to the private-transport status exactly as
// NewService does, and creates one circle_hub on it.
func newCircleIdentityHub(t *testing.T, relayURL string) *circleIdentityHub {
	t.Helper()
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(ts.Remove)
	require.NoError(t, ts.Cfg.SetUpdate("Relay", relayURL, ""))

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)
	svc := &service{
		cfg: ts.Cfg, keys: ts.Keys, db: ts.DB, lnClient: ln,
		nip47Service: nip47.NewNip47Service(ts.DB, ts.Cfg, ts.Keys, ts.EventPublisher, nil),
	}
	svc.nip47Service.SetPrivateIdentity(svc.privateJoinIdentity)

	hub, secret, err := ts.AppsService.CreateCircleHub("Circle Hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "Circle Hub", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 7200, PerWalletMaxMloki: 1_000_000})
	require.NoError(t, err)
	return &circleIdentityHub{svc: svc, ln: ln, walletPubkey: *hub.WalletPubkey, clientSecret: secret}
}

// circleWallet sends get_info over the standard transport, as a prospective member does,
// and returns the raw circle_wallet block.
func (h *circleIdentityHub) circleWallet(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	clientPubkey, err := nostr.GetPublicKey(h.clientSecret)
	require.NoError(t, err)
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, h.walletPubkey, h.clientSecret)
	require.NoError(t, err)
	content, err := c.Encrypt(`{"method":"get_info","params":{}}`)
	require.NoError(t, err)
	request := &nostr.Event{
		Kind: models.REQUEST_KIND, PubKey: clientPubkey, CreatedAt: nostr.Now(),
		Tags:    nostr.Tags{{"encryption", constants.ENCRYPTION_TYPE_NIP44_V2}, {"p", h.walletPubkey}},
		Content: content,
	}
	require.NoError(t, request.Sign(h.clientSecret))

	pool := tests.NewMockSimplePool()
	h.svc.nip47Service.HandleEvent(context.TODO(), pool, request, h.svc.lnClient)
	require.Len(t, pool.PublishedEvents, 1)
	plain, err := c.Decrypt(pool.PublishedEvents[0].Content)
	require.NoError(t, err)

	var response struct {
		Error  *models.Error `json:"error"`
		Result struct {
			CircleWallet map[string]json.RawMessage `json:"circle_wallet"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(plain), &response))
	require.Nil(t, response.Error)
	require.NotNil(t, response.Result.CircleWallet, "a circle_hub always answers with circle_wallet")
	return response.Result.CircleWallet
}

// NIP-CW §The Circle Wallet Hub Connection: identity is always present, the node
// identity that signs the kind-11190 announcement, whether or not the private
// transport has started.
func TestGetInfo_CircleIdentityIsTheAnnouncementSigner(t *testing.T) {
	relay := newCapturingRelay(t)
	h := newCircleIdentityHub(t, relay.url)

	before := h.circleWallet(t)
	require.Contains(t, before, "identity", "identity is reported always")

	pt, err := h.svc.resolvePrivateTransport()
	require.NoError(t, err)
	require.NoError(t, h.svc.publishTransportAnnouncement(context.TODO(), nostr.NewSimplePool(context.TODO()), pt))
	events := relay.received()
	require.Len(t, events, 1)
	announcement := events[0]
	require.Equal(t, transport.KindHubAnnouncement, announcement.Kind)

	block := h.circleWallet(t)
	var identity, earlier string
	require.NoError(t, json.Unmarshal(block["identity"], &identity))
	require.NoError(t, json.Unmarshal(before["identity"], &earlier))
	assert.Equal(t, announcement.Pubkey, identity, "identity must be the announcement's signer")
	assert.Equal(t, identity, earlier, "the same identity before and after the transport starts")
	assert.Regexp(t, `^[0-9a-f]{64}$`, identity)
	assert.NotEqual(t, pt.inboxXOnly, identity, "the inbox key is not the identity")
}
