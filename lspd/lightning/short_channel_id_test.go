package lightning

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShortChannelID_RoundTrip(t *testing.T) {
	tests := []string{
		"0x0x0",
		"1x2x3",
		"800000x1234x1",
		"16000000x0x0", // within the flnd alias block-height range used for phantom SCIDs
	}

	for _, s := range tests {
		t.Run(s, func(t *testing.T) {
			scid, err := NewShortChannelIDFromString(s)
			require.NoError(t, err)
			require.NotNil(t, scid)
			assert.Equal(t, s, scid.ToString(), "parsing then formatting must round-trip exactly")
		})
	}
}

func TestShortChannelID_EmptyStringIsZero(t *testing.T) {
	scid, err := NewShortChannelIDFromString("")
	require.NoError(t, err)
	require.NotNil(t, scid)
	assert.Equal(t, ShortChannelID(0), *scid)
}

func TestShortChannelID_InvalidFormats(t *testing.T) {
	tests := []string{
		"1x2",     // too few fields
		"1x2x3x4", // too many fields
		"ax2x3",   // non-numeric block height
		"1xbx3",   // non-numeric tx index
		"1x2xc",   // non-numeric output index
	}

	for _, s := range tests {
		t.Run(s, func(t *testing.T) {
			_, err := NewShortChannelIDFromString(s)
			assert.Error(t, err)
		})
	}
}

func TestShortChannelID_ToString_NilReceiver(t *testing.T) {
	var scid *ShortChannelID
	assert.Equal(t, "", scid.ToString())
}
