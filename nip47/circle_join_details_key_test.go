package nip47

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/nip47/cipher"
)

// NIP-CW §Response: encrypted_details is encrypted from the Hub's connection key.
// Pinned against the two keys a refactor of the private path could reach for
// instead: the inbox key the envelope is encrypted to, and the node identity that
// signs the announcement.
func TestCircleJoin_EncryptedDetailsFromHubConnectionKey(t *testing.T) {
	privateMember := nostr.GeneratePrivateKey()
	privatePub, _ := nostr.GetPublicKey(privateMember)
	h := newJoinHub(t, privatePub)

	nodeXOnly, err := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, err)
	h.hubXOnly = nodeXOnly
	inboxPriv, err := h.ts.Keys.GetPrivateTransportKey(0)
	require.NoError(t, err)
	inboxXOnly, err := nostr.GetPublicKey(inboxPriv)
	require.NoError(t, err)
	hubWalletKey, err := h.ts.Keys.GetAppWalletKey(h.app.ID)
	require.NoError(t, err)
	hubWalletPub, err := nostr.GetPublicKey(hubWalletKey)
	require.NoError(t, err)
	require.Equal(t, *h.app.WalletPubkey, hubWalletPub, "the Hub connection key is the circle_hub's app wallet key")

	res, served := h.serve(t, "p", joinParams(t, privateMember, hubWalletPub, 20_000), privateMember, strings.Repeat("0a", 32))
	require.True(t, served)
	require.Nil(t, res.Error, "%+v", res.Error)
	var body struct {
		EncryptedDetails string `json:"encrypted_details"`
	}
	require.NoError(t, json.Unmarshal(res.Result, &body))
	require.NotEmpty(t, body.EncryptedDetails)
	opensFrom := func(senderPub string) bool {
		c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, senderPub, privateMember)
		require.NoError(t, err)
		_, err = c.Decrypt(body.EncryptedDetails)
		return err == nil
	}
	assert.True(t, opensFrom(hubWalletPub), "must be encrypted from the Hub connection key")
	assert.False(t, opensFrom(inboxXOnly), "must not be encrypted from the inbox key")
	assert.False(t, opensFrom(nodeXOnly), "must not be encrypted from the node key")
}
