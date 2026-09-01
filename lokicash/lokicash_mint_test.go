package lokicash

import (
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signMintPayload produces a real recoverable signature over the canonical
// mint payload, exactly the way LND's SignMessage does (compact signature over
// the double-SHA256 of the message), so VerifyMint's recovery path is
// exercised end to end rather than mocked.
func signMintPayload(t *testing.T, priv *btcec.PrivateKey, hrp, walletPubkeyHex string, amount uint64) []byte {
	t.Helper()
	// Mirror the flnd node: prepend LNSignedMessagePrefix before double-hashing.
	digest := chainhash.DoubleHashB([]byte(LNSignedMessagePrefix + MintPayload(hrp, walletPubkeyHex, amount)))
	sig := ecdsa.SignCompact(priv, digest, true)
	require.Len(t, sig, mintSigLen)
	return sig
}

func minterKey(t *testing.T) (*btcec.PrivateKey, string) {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	return priv, hex.EncodeToString(priv.PubKey().SerializeCompressed())
}

func TestMintProvenance_RoundTripAndVerify(t *testing.T) {
	priv, minterPubkey := minterKey(t)
	const amount uint64 = 40000
	sig := signMintPayload(t, priv, HRP, testPubkey(), amount)

	amt := amount
	in := Token{
		HRP:            HRP,
		WalletPubkey:   testPubkey(),
		Secret:         testSecret(),
		RelayURLs:      []string{"wss://relay.example.com"},
		MintSignature:  sig,
		AttestedAmount: &amt,
	}
	encoded, err := Encode(in)
	require.NoError(t, err)

	out, err := Decode(encoded)
	require.NoError(t, err)
	require.NotNil(t, out.MintSignature)
	require.NotNil(t, out.AttestedAmount)
	assert.Equal(t, amount, *out.AttestedAmount)
	assert.Equal(t, sig, out.MintSignature)

	recovered, ok := VerifyMint(out)
	require.True(t, ok)
	assert.Equal(t, minterPubkey, recovered)
}

func TestMintProvenance_AbsentIsBackwardCompatible(t *testing.T) {
	// A plain token (no provenance) decodes with nil provenance fields and
	// VerifyMint reports no provenance — never an error.
	encoded, err := Encode(Token{HRP: HRP, WalletPubkey: testPubkey(), Secret: testSecret()})
	require.NoError(t, err)

	out, err := Decode(encoded)
	require.NoError(t, err)
	assert.Nil(t, out.MintSignature)
	assert.Nil(t, out.AttestedAmount)

	_, ok := VerifyMint(out)
	assert.False(t, ok)
}

func TestEncode_RejectsHalfProvenancePair(t *testing.T) {
	priv, _ := minterKey(t)
	sig := signMintPayload(t, priv, HRP, testPubkey(), 100)
	amt := uint64(100)

	// Signature without amount.
	_, err := Encode(Token{HRP: HRP, WalletPubkey: testPubkey(), Secret: testSecret(), MintSignature: sig})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set together")

	// Amount without signature.
	_, err = Encode(Token{HRP: HRP, WalletPubkey: testPubkey(), Secret: testSecret(), AttestedAmount: &amt})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set together")
}

func TestEncode_RejectsWrongLengthSignature(t *testing.T) {
	amt := uint64(100)
	_, err := Encode(Token{
		HRP:            HRP,
		WalletPubkey:   testPubkey(),
		Secret:         testSecret(),
		MintSignature:  make([]byte, mintSigLen-1),
		AttestedAmount: &amt,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint signature must be")
}

func TestDecode_LoneProvenanceHalfYieldsNoProvenance(t *testing.T) {
	base := []rawEntry{
		{typ: tlvWalletPubkey, value: mustHex(t, testPubkey())},
		{typ: tlvSecret, value: mustHex(t, testSecret())},
	}
	amountBytes := make([]byte, attestedAmountLen)
	binary.BigEndian.PutUint64(amountBytes, 100)

	// Only a signature, no amount.
	sigOnly := encodeRaw(t, HRP, rawTLV(t, append(append([]rawEntry(nil), base...),
		rawEntry{typ: tlvMintSignature, value: make([]byte, mintSigLen)})))
	out, err := Decode(sigOnly)
	require.NoError(t, err) // never a hard failure
	assert.Nil(t, out.MintSignature)
	assert.Nil(t, out.AttestedAmount)

	// Only an amount, no signature.
	amtOnly := encodeRaw(t, HRP, rawTLV(t, append(append([]rawEntry(nil), base...),
		rawEntry{typ: tlvAttestedAmount, value: amountBytes})))
	out, err = Decode(amtOnly)
	require.NoError(t, err)
	assert.Nil(t, out.MintSignature)
	assert.Nil(t, out.AttestedAmount)
}

func TestDecode_MalformedProvenanceYieldsNoProvenance(t *testing.T) {
	base := []rawEntry{
		{typ: tlvWalletPubkey, value: mustHex(t, testPubkey())},
		{typ: tlvSecret, value: mustHex(t, testSecret())},
	}
	amountBytes := make([]byte, attestedAmountLen)
	binary.BigEndian.PutUint64(amountBytes, 100)

	cases := map[string][]rawEntry{
		"wrong-length signature": {
			{typ: tlvMintSignature, value: make([]byte, mintSigLen-3)},
			{typ: tlvAttestedAmount, value: amountBytes},
		},
		"wrong-length amount": {
			{typ: tlvMintSignature, value: make([]byte, mintSigLen)},
			{typ: tlvAttestedAmount, value: make([]byte, attestedAmountLen-1)},
		},
		"duplicate signature": {
			{typ: tlvMintSignature, value: make([]byte, mintSigLen)},
			{typ: tlvMintSignature, value: make([]byte, mintSigLen)},
			{typ: tlvAttestedAmount, value: amountBytes},
		},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			encoded := encodeRaw(t, HRP, rawTLV(t, append(append([]rawEntry(nil), base...), extra...)))
			out, err := Decode(encoded)
			require.NoError(t, err) // provenance anomalies never brick a decode
			assert.Nil(t, out.MintSignature)
			assert.Nil(t, out.AttestedAmount)
		})
	}
}

func TestVerifyMint_TamperedAmountDoesNotRecoverSigner(t *testing.T) {
	priv, minterPubkey := minterKey(t)
	sig := signMintPayload(t, priv, HRP, testPubkey(), 40000)

	// Signature is over amount 40000, but the token claims 41000. Recovery
	// still succeeds (a 65-byte compact sig always recovers *some* key), but
	// over the tampered payload it yields a different key — so the caller's
	// equality check against the expected minter fails.
	tampered := uint64(41000)
	recovered, ok := VerifyMint(Token{
		HRP:            HRP,
		WalletPubkey:   testPubkey(),
		MintSignature:  sig,
		AttestedAmount: &tampered,
	})
	require.True(t, ok)
	assert.NotEqual(t, minterPubkey, recovered)
}

func TestVerifyMint_MalformedSignatureReturnsFalse(t *testing.T) {
	amt := uint64(100)
	// An all-zero header byte is an invalid recovery id, so RecoverCompact
	// errors and VerifyMint reports no valid provenance.
	_, ok := VerifyMint(Token{
		HRP:            HRP,
		WalletPubkey:   testPubkey(),
		MintSignature:  make([]byte, mintSigLen),
		AttestedAmount: &amt,
	})
	assert.False(t, ok)
}
