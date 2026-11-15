package keys

import (
	"strconv"
	"testing"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/tests/db"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const transportTestMnemonic = "thought turkey ask pottery head say catalog desk pledge elbow naive mimic"

func newTransportTestKeys(t *testing.T) Keys {
	t.Helper()
	logger.Init(strconv.Itoa(4))

	gormDb, err := db.NewDB(t)
	require.NoError(t, err)
	t.Cleanup(func() { db.CloseDB(gormDb) })

	cfg, err := config.NewConfig(&config.AppConfig{}, gormDb)
	require.NoError(t, err)
	require.NoError(t, cfg.SetUpdate("Mnemonic", transportTestMnemonic, "pw"))

	k := NewKeys()
	require.NoError(t, k.Init(cfg, "pw"))
	return k
}

// TestGetPrivateTransportKey_IsDeterministic is what lets the key be seed-derived
// instead of stored: the hub must recover the same inbox key after a restart, or
// every bill in circulation would suddenly be encrypting to a key it no longer
// holds.
func TestGetPrivateTransportKey_IsDeterministic(t *testing.T) {
	k := newTransportTestKeys(t)

	first, err := k.GetPrivateTransportKey(0)
	require.NoError(t, err)
	second, err := k.GetPrivateTransportKey(0)
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Len(t, first, 64, "a private key is 32 bytes of hex")
}

// TestGetPrivateTransportKey_RotatesByIndex is the property the index exists for.
// Without it, a leaked inbox key would expose all future private traffic for every
// bill in circulation and reissuing every bill would be the only remedy.
func TestGetPrivateTransportKey_RotatesByIndex(t *testing.T) {
	k := newTransportTestKeys(t)

	seen := map[string]uint32{}
	for index := uint32(0); index < 8; index++ {
		priv, err := k.GetPrivateTransportKey(index)
		require.NoError(t, err)
		if prev, dup := seen[priv]; dup {
			t.Fatalf("index %d derived the same key as index %d", index, prev)
		}
		seen[priv] = index
	}
}

// TestGetPrivateTransportKey_IndependentOfOtherBranches pins the branch separation.
// H+1 serves app wallets and H+2 serves Cash pairing keys; a collision would mean
// the hub's inbox key was also somebody's bill key, so holding a bill would let its
// holder decrypt every other bill's traffic.
func TestGetPrivateTransportKey_IndependentOfOtherBranches(t *testing.T) {
	k := newTransportTestKeys(t)

	transport, err := k.GetPrivateTransportKey(0)
	require.NoError(t, err)

	// Same index across every branch, which is where a path bug would show up.
	for index := uint(0); index < 4; index++ {
		appWallet, err := k.GetAppWalletKey(index)
		require.NoError(t, err)
		assert.NotEqual(t, transport, appWallet,
			"transport key collided with the app-wallet branch at index %d", index)

		pairing, err := k.GetCashPairingKey(index)
		require.NoError(t, err)
		assert.NotEqual(t, transport, pairing,
			"transport key collided with the Cash pairing branch at index %d", index)
	}

	// And it must not be the hub's own (deprecated, randomly generated) Nostr key.
	assert.NotEqual(t, transport, k.GetNostrSecretKey())
}

// TestGetPrivateTransportKey_YieldsAUsableNostrPubkey checks the derived key is
// valid as a Nostr identity, since the announcement publishes its x-only form and
// clients NIP-44 encrypt to exactly that.
func TestGetPrivateTransportKey_YieldsAUsableNostrPubkey(t *testing.T) {
	k := newTransportTestKeys(t)

	priv, err := k.GetPrivateTransportKey(0)
	require.NoError(t, err)

	pub, err := nostr.GetPublicKey(priv)
	require.NoError(t, err)
	assert.Len(t, pub, 64, "an x-only Nostr pubkey is 32 bytes of hex")

	// Stable alongside the private key, so a restart announces the same inbox.
	again, err := nostr.GetPublicKey(priv)
	require.NoError(t, err)
	assert.Equal(t, pub, again)
}
