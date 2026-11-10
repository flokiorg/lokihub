package pubkeyhex_test

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/nostr/pubkeyhex"
)

var good = strings.Repeat("ab", 32)

func TestValid(t *testing.T) {
	assert.True(t, pubkeyhex.Valid(good))
	assert.True(t, pubkeyhex.Valid(strings.Repeat("0", 64)))

	for name, in := range map[string]string{
		"empty":     "",
		"too short": good[:62],
		"too long":  good + "aa",
		"uppercase": strings.ToUpper(good),
		"not hex":   strings.Repeat("zz", 32),
		"spaces":    " " + good[:62] + " ",
		"npub":      "npub1" + strings.Repeat("q", 59),
	} {
		assert.False(t, pubkeyhex.Valid(in), name)
	}
}

func TestNormalize(t *testing.T) {
	assert.Equal(t, good, pubkeyhex.Normalize("  "+strings.ToUpper(good)+"\n"))
	assert.True(t, pubkeyhex.Valid(pubkeyhex.Normalize(" "+strings.ToUpper(good))))
}

// The owner-published fiat-rate oracle npub decodes to this hex (the confirmed
// addressable 30078:<hex>:flokicoin-fiat-rate author).
func TestFromNpub_RealOracleKey(t *testing.T) {
	const (
		npub    = "npub1eywl4czqunqp69mal68as5mhy4jfzjwxwztrd63pgycelr3jhzgs0nlnt8"
		wantHex = "c91dfae040e4c01d177dfe8fd8537725649149c6709636ea2141319f8e32b891"
	)
	got, err := pubkeyhex.FromNpub(npub)
	require.NoError(t, err)
	assert.Equal(t, wantHex, got)
}

func TestFromNpub(t *testing.T) {
	npub, err := nip19.EncodePublicKey(good)
	require.NoError(t, err)

	got, err := pubkeyhex.FromNpub(npub)
	require.NoError(t, err)
	assert.Equal(t, good, got)

	got, err = pubkeyhex.FromNpub("  " + npub + " ")
	require.NoError(t, err)
	assert.Equal(t, good, got, "surrounding space is tolerated")

	nsec, err := nip19.EncodePrivateKey(good)
	require.NoError(t, err)
	_, err = pubkeyhex.FromNpub(nsec)
	assert.Error(t, err, "an nsec is not an npub")

	_, err = pubkeyhex.FromNpub("npub1notvalid")
	assert.Error(t, err)
	_, err = pubkeyhex.FromNpub("")
	assert.Error(t, err)
}
