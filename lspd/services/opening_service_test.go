package services

import (
	"testing"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testKeypair(t *testing.T) (*btcec.PrivateKey, *btcec.PublicKey) {
	t.Helper()
	priv, pub := btcec.PrivKeyFromBytes([]byte("0123456789abcdef0123456789abcde"))
	return priv, pub
}

func sampleParams() *core.OpeningFeeParams {
	return &core.OpeningFeeParams{
		MinFeeMloki:          1000,
		Proportional:         4000,
		ValidUntil:           "2099-01-01T00:00:00.000Z",
		MinLifetime:          43200,
		MaxClientToSelfDelay: 4320,
		MinPaymentSizeMloki:  0,
		MaxPaymentSizeMloki:  10_000_000_000,
	}
}

// TestCreateAndVerifyPromise_RoundTrip is the sign/verify round trip for
// LSPS2 opening_fee_params "promises": the LSP signs a hash of the params
// it offers, and later must verify a client-returned promise really came
// from its own key over exactly those params before honoring a JIT open.
func TestCreateAndVerifyPromise_RoundTrip(t *testing.T) {
	priv, pub := testKeypair(t)
	params := sampleParams()

	promise, err := createPromise(priv, params)
	require.NoError(t, err)
	require.NotNil(t, promise)
	params.Promise = *promise

	err = verifyPromise(pub, params)
	assert.NoError(t, err, "a promise signed by priv must verify against the matching pub")
}

func TestVerifyPromise_RejectsWrongSigner(t *testing.T) {
	priv, _ := testKeypair(t)
	_, otherPub := btcec.PrivKeyFromBytes([]byte("zyxwvutsrqponmlkjihgfedcba098765"))
	params := sampleParams()

	promise, err := createPromise(priv, params)
	require.NoError(t, err)
	params.Promise = *promise

	err = verifyPromise(otherPub, params)
	assert.Error(t, err, "a promise must not verify against a different LSP's public key")
}

func TestVerifyPromise_RejectsTamperedParams(t *testing.T) {
	priv, pub := testKeypair(t)
	params := sampleParams()

	promise, err := createPromise(priv, params)
	require.NoError(t, err)
	params.Promise = *promise

	// Tamper with a field after signing — the promise must no longer
	// verify, since it commits to the exact param values.
	params.MinFeeMloki = 999999

	err = verifyPromise(pub, params)
	assert.Error(t, err, "a promise must not verify once the signed params have been tampered with")
}

func TestVerifyPromise_RejectsMalformedPromise(t *testing.T) {
	_, pub := testKeypair(t)
	params := sampleParams()
	params.Promise = "not-valid-hex"

	err := verifyPromise(pub, params)
	assert.Error(t, err)
}

func TestParamsHash_DeterministicAndOrderSensitive(t *testing.T) {
	p1 := sampleParams()
	p2 := sampleParams()

	h1, err := paramsHash(p1)
	require.NoError(t, err)
	h2, err := paramsHash(p2)
	require.NoError(t, err)
	assert.Equal(t, h1, h2, "hashing identical params must be deterministic")

	p2.Proportional = p1.Proportional + 1
	h3, err := paramsHash(p2)
	require.NoError(t, err)
	assert.NotEqual(t, h1, h3, "changing any field must change the hash")
}

func TestValidateOpeningFeeParams_ExpiredIsRejected(t *testing.T) {
	svc := &openingService{}
	priv, pub := testKeypair(t)
	params := sampleParams()
	params.ValidUntil = "2020-01-01T00:00:00.000Z" // long past
	promise, err := createPromise(priv, params)
	require.NoError(t, err)
	params.Promise = *promise

	valid := svc.ValidateOpeningFeeParams(params, pub)
	assert.False(t, valid, "expired opening_fee_params must be rejected even with a valid signature")
}

func TestValidateOpeningFeeParams_NilParams(t *testing.T) {
	svc := &openingService{}
	_, pub := testKeypair(t)
	assert.False(t, svc.ValidateOpeningFeeParams(nil, pub))
}
