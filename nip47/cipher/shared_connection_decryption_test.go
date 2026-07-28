package cipher

import (
	"encoding/json"
	"testing"

	"github.com/flokiorg/lokihub/constants"
	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"
)

// TestSharedConnectionSecret_AnyHolderCanDecryptAnyResponse documents the
// underlying cryptographic property that makes a jit_wallet's shared
// connection shared in the first place — and why jit_transfer's response
// (nip47/controllers/jit_transfer_controller.go) is designed to never carry
// a server-generated secret, only caller-supplied commitments and public
// identities.
//
// A jit_wallet's NWC connection is deliberately shared among all recipients:
// jitwallet.Commit hands out one pairing URI / lokicash token for the whole
// group, and every recipient (and the hub operator) holds the SAME client
// secret. nip47 responses are encrypted with a NIP-44 conversation key
// derived purely from (client pubkey, wallet privkey) — see
// event_handler.go's NewNip47Cipher(encryption, app.AppPubkey,
// appWalletPrivKey). By ECDH symmetry, anyone holding the client PRIVKEY
// (which every recipient does) derives the identical conversation key and
// can decrypt EVERY response on that connection, not just their own
// request's response.
//
// This was originally found (2026-07-28 independent audit) as an exploit
// path: jit_transfer used to mint a fresh bearer secret server-side and
// return it in this exact response shape — decryptable by any co-recipient,
// who could then redeem the slice before its intended holder. The fix
// (jit_transfer_controller.go) is to never put a secret in a response on
// this channel: a bearer target's identity_value is now a caller-supplied
// commitment the caller generated and kept themselves, never something the
// wallet reveals here. This test keeps the underlying property under test —
// any secret placed in a response on this channel WILL leak — so that
// property can't silently regress without a test failing to prompt someone
// to re-derive why it matters.
func TestSharedConnectionSecret_AnyHolderCanDecryptAnyResponse(t *testing.T) {
	// The wallet's server-side nostr key.
	walletPriv := nostr.GeneratePrivateKey()
	walletPub, err := nostr.GetPublicKey(walletPriv)
	require.NoError(t, err)

	// The single shared client secret embedded in the pairing URI / lokicash
	// token that jitwallet.Commit distributes to the WHOLE recipient group.
	clientPriv := nostr.GeneratePrivateKey()
	clientPub, err := nostr.GetPublicKey(clientPriv)
	require.NoError(t, err)

	// How the hub encrypts any response on this connection (event_handler.go):
	// NewNip47Cipher(encryption, app.AppPubkey /* = clientPub */, appWalletPrivKey /* = walletPriv */).
	walletSide, err := NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, clientPub, walletPriv)
	require.NoError(t, err)

	// Stand-in for any hypothetical secret a response might carry — this
	// codebase no longer puts one here (see jit_transfer_controller.go), but
	// the point of this test is that if it ever did, it would leak.
	const hypotheticalSecret = "b8f2c1a09d7e6f5a4b3c2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f10"
	responseJSON, err := json.Marshal(map[string]any{
		"amount_mloki":  100000,
		"identity_type": "bearer",
		"secret":        hypotheticalSecret,
	})
	require.NoError(t, err)

	ciphertext, err := walletSide.Encrypt(string(responseJSON))
	require.NoError(t, err)

	// A DIFFERENT party — e.g. a co-recipient who already redeemed their own
	// slice — holds only the shared client secret, never any victim-specific
	// key. They reconstruct the identical conversation key from (walletPub,
	// clientPriv) and decrypt the response.
	otherHolderSide, err := NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, walletPub, clientPriv)
	require.NoError(t, err)

	decrypted, err := otherHolderSide.Decrypt(ciphertext)
	require.NoError(t, err)

	var recovered map[string]any
	require.NoError(t, json.Unmarshal([]byte(decrypted), &recovered))

	require.Equal(t, hypotheticalSecret, recovered["secret"],
		"any holder of the shared connection secret can decrypt any response on it — a secret placed in a response here is not private to the caller who requested it")
}
