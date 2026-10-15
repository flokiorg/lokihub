package controllers

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CW §Creating a Circle Wallet: pubkey is REQUIRED and MUST be a
// 64-character lowercase-hex Nostr pubkey (CW-creating-a-circle-wallet-01).
// The format check answers first; later checks would also refuse some of these,
// but for a different reason.
func TestCreateCircleWallet_MalformedPubkeyRefusedAsSuch(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)

	memberPriv := nostr.GeneratePrivateKey()
	member, _ := nostr.GetPublicKey(memberPriv)
	proof := mustMarshal(t, buildCircleWalletIdentityEvent(t, memberPriv, *provider.WalletPubkey))

	for name, pubkey := range map[string]string{
		"uppercase": strings.ToUpper(member),
		"too short": member[:63],
		"not hex":   "zz" + member[2:],
	} {
		response := callCreateCircleWallet(t, svc, provider, rawCircleWalletRequest(t, pubkey, 100_000, 3600, proof))
		require.NotNil(t, response.Error, name)
		assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code, name)
		assert.Contains(t, response.Error.Message, "lowercase-hex", name)
	}
}
