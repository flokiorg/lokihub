package lightning

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutPoint_RoundTrip(t *testing.T) {
	// chainhash.Hash.String() reverses byte order for display (the usual
	// txid RPC convention), so the fixture must not be a byte-order
	// palindrome or a round-trip bug could hide behind symmetry.
	txid := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	original := txid + ":3"
	op, err := NewOutPointFromString(original)
	require.NoError(t, err)
	assert.Equal(t, original, op.String())
}

// TestOutPoint_StringAndBytesConstructorsAgree pins the fix for a byte-order
// bug: NewOutPointFromString parses a display-order txid string (as LND's
// FundingTxidStr / ChannelPoint fields provide), while NewOutPoint takes raw
// wire-order bytes (as FundingTxidBytes provides). Both describe the same
// funding transaction, so they must produce identical wire.OutPoint values —
// otherwise code paths that populate one via bytes and another via string
// (as flspd's channel-sync vs. htlc-resolution paths do) silently diverge and
// bytes.Compare-based correlation (e.g. in the revenue CLI) breaks.
func TestOutPoint_StringAndBytesConstructorsAgree(t *testing.T) {
	displayTxid := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"[:64]
	wireBytes, err := hex.DecodeString(displayTxid)
	require.NoError(t, err)
	// reverse to get the raw wire-order bytes this display string represents
	reversed := make([]byte, len(wireBytes))
	for i, b := range wireBytes {
		reversed[len(wireBytes)-1-i] = b
	}

	fromString, err := NewOutPointFromString(displayTxid + ":7")
	require.NoError(t, err)

	fromBytes, err := NewOutPoint(reversed, 7)
	require.NoError(t, err)

	assert.Equal(t, fromBytes.Hash, fromString.Hash, "parsing a display-order string must match constructing from the equivalent wire-order bytes")
}

func TestOutPoint_InvalidFormats(t *testing.T) {
	tests := []string{
		"missing-colon",
		"deadbeef:not-a-number",
		"not-hex:0",
		"deadbeef:0:extra",
	}

	for _, s := range tests {
		t.Run(s, func(t *testing.T) {
			_, err := NewOutPointFromString(s)
			assert.Error(t, err)
		})
	}
}
