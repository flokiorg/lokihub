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

// NIP-CW §Response: encrypted_details is encrypted from the Hub's connection key on either
// transport. Pinned against the two keys a refactor of the private path could reach for
// instead: the inbox key the envelope is encrypted to, and the node identity that signs
// the announcement.
func TestCircleJoin_EncryptedDetailsFromHubConnectionKeyOnBothTransports(t *testing.T) {
	privateMember, standardMember := nostr.GeneratePrivateKey(), nostr.GeneratePrivateKey()
	privatePub, _ := nostr.GetPublicKey(privateMember)
	standardPub, _ := nostr.GetPublicKey(standardMember)
	h := newJoinHub(t, privatePub, standardPub)

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
	var privateBody struct {
		EncryptedDetails string `json:"encrypted_details"`
	}
	require.NoError(t, json.Unmarshal(res.Result, &privateBody))

	response := sendOverStandardTransport(t, h.ts, h.svc, hubWalletPub, h.connPriv, map[string]interface{}{
		"method": constants.NIP47MethodCreateCircleWallet,
		"params": json.RawMessage(joinParams(t, standardMember, hubWalletPub, 20_000)),
	})
	require.Nil(t, response.Error, "%+v", response.Error)
	raw, err := json.Marshal(response.Result)
	require.NoError(t, err)
	var standardBody struct {
		EncryptedDetails string `json:"encrypted_details"`
	}
	require.NoError(t, json.Unmarshal(raw, &standardBody))

	for _, tc := range []struct {
		transport, memberPriv, ciphertext string
	}{
		{"private", privateMember, privateBody.EncryptedDetails},
		{"standard", standardMember, standardBody.EncryptedDetails},
	} {
		require.NotEmpty(t, tc.ciphertext, tc.transport)
		opensFrom := func(senderPub string) bool {
			c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, senderPub, tc.memberPriv)
			require.NoError(t, err)
			_, err = c.Decrypt(tc.ciphertext)
			return err == nil
		}
		assert.True(t, opensFrom(hubWalletPub), "%s: must be encrypted from the Hub connection key", tc.transport)
		assert.False(t, opensFrom(inboxXOnly), "%s: must not be encrypted from the inbox key", tc.transport)
		assert.False(t, opensFrom(nodeXOnly), "%s: must not be encrypted from the node key", tc.transport)
	}
}
