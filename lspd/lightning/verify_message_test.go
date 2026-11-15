package lightning

import (
	"testing"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyMessage_RoundTrip(t *testing.T) {
	priv, pub := btcec.PrivKeyFromBytes([]byte("0123456789abcdef0123456789abcde"))
	message := []byte("hello lightning")

	sig, err := SignLsps5Message(priv, "2026-01-01T00:00:00.000Z", message)
	require.NoError(t, err)

	// SignLsps5Message signs the bLIP-55 wrapped message, so verification
	// must reconstruct the exact same wrapped string.
	wrapped := []byte("LSPS5: DO NOT SIGN THIS MESSAGE MANUALLY: LSP: At 2026-01-01T00:00:00.000Z I notify hello lightning")
	recoveredPub, err := VerifyMessage(wrapped, sig)
	require.NoError(t, err)
	assert.True(t, pub.IsEqual(recoveredPub))
}

func TestVerifyMessage_InvalidSignatureEncoding(t *testing.T) {
	_, err := VerifyMessage([]byte("anything"), "not valid zbase32!!!")
	assert.Error(t, err)
}

func TestVerifyMessage_WrongMessageFailsOrRecoversDifferentKey(t *testing.T) {
	priv, pub := btcec.PrivKeyFromBytes([]byte("0123456789abcdef0123456789abcde"))
	sig, err := SignLsps5Message(priv, "2026-01-01T00:00:00.000Z", []byte("original"))
	require.NoError(t, err)

	wrongWrapped := []byte("LSPS5: DO NOT SIGN THIS MESSAGE MANUALLY: LSP: At 2026-01-01T00:00:00.000Z I notify tampered")
	recoveredPub, err := VerifyMessage(wrongWrapped, sig)
	// Signature recovery over a different message either errors or
	// recovers a different (wrong) pubkey — either way it must not
	// validate as the original signer.
	if err == nil {
		assert.False(t, pub.IsEqual(recoveredPub), "a signature must not verify against a different message with the same pubkey")
	}
}
