package lokicash

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPubkey() string {
	return strings.Repeat("ab", keyLen)
}

func testSecret() string {
	return strings.Repeat("cd", keyLen)
}

func TestEncodeDecode_RoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		relays []string
	}{
		{"no relays", nil},
		{"one relay", []string{"wss://relay.getalby.com/v1"}},
		{"several relays, order preserved", []string{"wss://relay.a", "wss://relay.b", "wss://relay.c"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := Token{
				HRP:          HRP,
				WalletPubkey: testPubkey(),
				Secret:       testSecret(),
				RelayURLs:    tc.relays,
			}
			encoded, err := Encode(in)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(encoded, HRP+"1"))

			out, err := Decode(encoded)
			require.NoError(t, err)
			assert.Equal(t, in.HRP, out.HRP)
			assert.Equal(t, in.WalletPubkey, out.WalletPubkey)
			assert.Equal(t, in.Secret, out.Secret)
			assert.Equal(t, tc.relays, out.RelayURLs)
		})
	}
}

func TestEncodeDecode_DifferentHRP(t *testing.T) {
	// Same TLV layout, different backing-asset prefix (NIP-JW §The Lokicash
	// Token) — Decode must not assume "lokicash" and must round-trip
	// whatever HRP it's given.
	in := Token{
		HRP:          "satscash",
		WalletPubkey: testPubkey(),
		Secret:       testSecret(),
		RelayURLs:    []string{"wss://relay.test"},
	}
	encoded, err := Encode(in)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(encoded, "satscash1"))

	out, err := Decode(encoded)
	require.NoError(t, err)
	assert.Equal(t, "satscash", out.HRP)
	assert.Equal(t, in.WalletPubkey, out.WalletPubkey)
	assert.Equal(t, in.Secret, out.Secret)
}

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

func TestEncodeDecode_RoundTrip_IdentityRequiredAndMaxTransfers(t *testing.T) {
	cases := []struct {
		name             string
		identityRequired *bool
		maxTransfers     *int
	}{
		{"neither set (old-token-shaped)", nil, nil},
		{"bearer, unlimited transfers", boolPtr(false), intPtr(0)},
		{"identity-bound, capped transfers", boolPtr(true), intPtr(3)},
		{"identity-bound set, transfers unset", boolPtr(true), nil},
		{"transfers set, identity unset", nil, intPtr(5)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := Token{
				HRP:              HRP,
				WalletPubkey:     testPubkey(),
				Secret:           testSecret(),
				IdentityRequired: tc.identityRequired,
				MaxTransfers:     tc.maxTransfers,
			}
			encoded, err := Encode(in)
			require.NoError(t, err)
			out, err := Decode(encoded)
			require.NoError(t, err)
			if tc.identityRequired == nil {
				assert.Nil(t, out.IdentityRequired)
			} else {
				require.NotNil(t, out.IdentityRequired)
				assert.Equal(t, *tc.identityRequired, *out.IdentityRequired)
			}
			if tc.maxTransfers == nil {
				assert.Nil(t, out.MaxTransfers)
			} else {
				require.NotNil(t, out.MaxTransfers)
				assert.Equal(t, *tc.maxTransfers, *out.MaxTransfers)
			}
		})
	}
}

func TestEncode_RejectsNegativeMaxTransfers(t *testing.T) {
	_, err := Encode(Token{HRP: HRP, WalletPubkey: testPubkey(), Secret: testSecret(), MaxTransfers: intPtr(-1)})
	assert.Error(t, err)
}

func TestDecode_RejectsMalformedIdentityRequired(t *testing.T) {
	t.Run("wrong length", func(t *testing.T) {
		raw := rawTLV(t, []rawEntry{
			{typ: tlvWalletPubkey, value: mustHex(t, testPubkey())},
			{typ: tlvSecret, value: mustHex(t, testSecret())},
			{typ: tlvIdentityRequired, value: []byte{0, 0}},
		})
		_, err := Decode(encodeRaw(t, HRP, raw))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "identity_required must be 1 byte")
	})
	t.Run("out of range value", func(t *testing.T) {
		raw := rawTLV(t, []rawEntry{
			{typ: tlvWalletPubkey, value: mustHex(t, testPubkey())},
			{typ: tlvSecret, value: mustHex(t, testSecret())},
			{typ: tlvIdentityRequired, value: []byte{2}},
		})
		_, err := Decode(encodeRaw(t, HRP, raw))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be 0 or 1")
	})
	t.Run("duplicate", func(t *testing.T) {
		raw := rawTLV(t, []rawEntry{
			{typ: tlvWalletPubkey, value: mustHex(t, testPubkey())},
			{typ: tlvSecret, value: mustHex(t, testSecret())},
			{typ: tlvIdentityRequired, value: []byte{1}},
			{typ: tlvIdentityRequired, value: []byte{0}},
		})
		_, err := Decode(encodeRaw(t, HRP, raw))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate identity_required")
	})
}

func TestDecode_RejectsMalformedMaxTransfers(t *testing.T) {
	t.Run("wrong length", func(t *testing.T) {
		raw := rawTLV(t, []rawEntry{
			{typ: tlvWalletPubkey, value: mustHex(t, testPubkey())},
			{typ: tlvSecret, value: mustHex(t, testSecret())},
			{typ: tlvMaxTransfers, value: []byte{0, 0, 0}},
		})
		_, err := Decode(encodeRaw(t, HRP, raw))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max_transfers must be 4 bytes")
	})
	t.Run("duplicate", func(t *testing.T) {
		raw := rawTLV(t, []rawEntry{
			{typ: tlvWalletPubkey, value: mustHex(t, testPubkey())},
			{typ: tlvSecret, value: mustHex(t, testSecret())},
			{typ: tlvMaxTransfers, value: []byte{0, 0, 0, 1}},
			{typ: tlvMaxTransfers, value: []byte{0, 0, 0, 2}},
		})
		_, err := Decode(encodeRaw(t, HRP, raw))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate max_transfers")
	})
}

func TestEncode_RejectsBadWalletPubkey(t *testing.T) {
	cases := []struct {
		name   string
		pubkey string
	}{
		{"not hex", "not-hex-at-all-not-hex-at-all-not-hex-at-all-not-hex-at-all-x"},
		{"too short", strings.Repeat("ab", keyLen-1)},
		{"too long", strings.Repeat("ab", keyLen+1)},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Encode(Token{HRP: HRP, WalletPubkey: tc.pubkey, Secret: testSecret()})
			assert.Error(t, err)
		})
	}
}

func TestEncode_RejectsBadSecret(t *testing.T) {
	cases := []struct {
		name   string
		secret string
	}{
		{"not hex", "zz"},
		{"too short", strings.Repeat("cd", keyLen-1)},
		{"too long", strings.Repeat("cd", keyLen+1)},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Encode(Token{HRP: HRP, WalletPubkey: testPubkey(), Secret: tc.secret})
			assert.Error(t, err)
		})
	}
}

func TestEncode_RejectsOversizeRelayURL(t *testing.T) {
	// A relay URL over 255 bytes can't fit a single TLV entry's one-byte
	// length field. Silently truncating that length would corrupt every
	// entry after it in the stream, so Encode must reject it outright.
	_, err := Encode(Token{
		HRP:          HRP,
		WalletPubkey: testPubkey(),
		Secret:       testSecret(),
		RelayURLs:    []string{"wss://" + strings.Repeat("x", 300)},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "relay url exceeds")
}

func TestDecode_RejectsInvalidBech32(t *testing.T) {
	cases := []string{
		"",
		"not a bech32 string",
		HRP + "1invalidchecksum",
		"lokicash1", // hrp + separator, no data, no valid checksum
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			_, err := Decode(s)
			assert.Error(t, err)
		})
	}
}

// TestDecode_SingleCharMutationAlwaysFails asserts the bech32 checksum
// catches every single-character corruption of a valid token — the
// guarantee that lets a lokicash token be handed over in a chat message or
// read aloud without a typo silently pairing the recipient with the wrong
// wallet or a mangled secret (NIP-JW §The Lokicash Token: "the token
// doesn't need to be kept secret", which only holds if a corrupted token
// reliably fails instead of decoding into something else that looks valid).
func TestDecode_SingleCharMutationAlwaysFails(t *testing.T) {
	valid, err := Encode(Token{
		HRP:          HRP,
		WalletPubkey: testPubkey(),
		Secret:       testSecret(),
		RelayURLs:    []string{"wss://relay.test"},
	})
	require.NoError(t, err)

	const charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	sepIdx := strings.LastIndex(valid, "1")
	require.Greater(t, sepIdx, -1)

	for i := sepIdx + 1; i < len(valid); i++ {
		original := valid[i]
		for _, c := range charset {
			if c == rune(original) {
				continue
			}
			mutated := valid[:i] + string(c) + valid[i+1:]
			_, err := Decode(mutated)
			assert.Error(t, err, "mutation at position %d (%q -> %q) should have been rejected: %s", i, valid, mutated, mutated)
		}
	}
}

func TestDecode_RejectsMissingWalletPubkey(t *testing.T) {
	// Hand-crafted: secret + relay, no type-0 entry at all.
	raw := rawTLV(t, []rawEntry{
		{typ: tlvSecret, value: mustHex(t, testSecret())},
		{typ: tlvRelay, value: []byte("wss://relay.test")},
	})
	_, err := Decode(encodeRaw(t, HRP, raw))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing wallet pubkey")
}

func TestDecode_RejectsMissingSecret(t *testing.T) {
	raw := rawTLV(t, []rawEntry{
		{typ: tlvWalletPubkey, value: mustHex(t, testPubkey())},
		{typ: tlvRelay, value: []byte("wss://relay.test")},
	})
	_, err := Decode(encodeRaw(t, HRP, raw))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing secret")
}

func TestDecode_RejectsWrongLengthWalletPubkey(t *testing.T) {
	raw := rawTLV(t, []rawEntry{
		{typ: tlvWalletPubkey, value: mustHex(t, testPubkey())[:31]}, // 31 bytes, not 32
		{typ: tlvSecret, value: mustHex(t, testSecret())},
	})
	_, err := Decode(encodeRaw(t, HRP, raw))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wallet pubkey must be")
}

func TestDecode_RejectsWrongLengthSecret(t *testing.T) {
	raw := rawTLV(t, []rawEntry{
		{typ: tlvWalletPubkey, value: mustHex(t, testPubkey())},
		{typ: tlvSecret, value: append(mustHex(t, testSecret()), 0x00)}, // 33 bytes
	})
	_, err := Decode(encodeRaw(t, HRP, raw))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret must be")
}

func TestDecode_RejectsDuplicateWalletPubkey(t *testing.T) {
	// Two different type-0 entries: which one is authoritative? Neither —
	// reject outright rather than silently picking one (first-wins/last-wins
	// would be an implementation-defined footgun for any two decoders that
	// disagree on which).
	otherPubkey := strings.Repeat("ef", keyLen)
	raw := rawTLV(t, []rawEntry{
		{typ: tlvWalletPubkey, value: mustHex(t, testPubkey())},
		{typ: tlvWalletPubkey, value: mustHex(t, otherPubkey)},
		{typ: tlvSecret, value: mustHex(t, testSecret())},
	})
	_, err := Decode(encodeRaw(t, HRP, raw))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate wallet pubkey")
}

func TestDecode_RejectsDuplicateSecret(t *testing.T) {
	otherSecret := strings.Repeat("ef", keyLen)
	raw := rawTLV(t, []rawEntry{
		{typ: tlvWalletPubkey, value: mustHex(t, testPubkey())},
		{typ: tlvSecret, value: mustHex(t, testSecret())},
		{typ: tlvSecret, value: mustHex(t, otherSecret)},
	})
	_, err := Decode(encodeRaw(t, HRP, raw))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate secret")
}

func TestDecode_RejectsTruncatedTLVEntry(t *testing.T) {
	// A type-1 (relay) entry that declares a length longer than the bytes
	// actually remaining in the stream. Must error cleanly, not panic or
	// read out of bounds.
	buf := &bytes.Buffer{}
	writeTLV(buf, tlvWalletPubkey, mustHex(t, testPubkey()))
	writeTLV(buf, tlvSecret, mustHex(t, testSecret()))
	buf.WriteByte(tlvRelay)
	buf.WriteByte(50) // claims 50 bytes of relay URL
	buf.WriteString("short")

	_, err := Decode(encodeRaw(t, HRP, buf.Bytes()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated TLV entry")
}

func TestDecode_IgnoresUnknownTLVTypeButKeepsParsing(t *testing.T) {
	// Forward compatibility: an unknown TLV type must be skipped, not
	// treated as an error, and parsing of the rest of the stream must still
	// succeed — mirrors NIP-19's own decoders.
	raw := rawTLV(t, []rawEntry{
		{typ: tlvWalletPubkey, value: mustHex(t, testPubkey())},
		{typ: 99, value: []byte("some-future-field")},
		{typ: tlvSecret, value: mustHex(t, testSecret())},
	})
	out, err := Decode(encodeRaw(t, HRP, raw))
	require.NoError(t, err)
	assert.Equal(t, testPubkey(), out.WalletPubkey)
	assert.Equal(t, testSecret(), out.Secret)
}

func TestDecode_EmptyPayload(t *testing.T) {
	_, err := Decode(encodeRaw(t, HRP, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing wallet pubkey")
}

// --- test helpers for constructing adversarial/malformed tokens that
// Encode itself would never produce ---

type rawEntry struct {
	typ   uint8
	value []byte
}

func rawTLV(t *testing.T, entries []rawEntry) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	for _, e := range entries {
		writeTLV(buf, e.typ, e.value)
	}
	return buf.Bytes()
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

func encodeRaw(t *testing.T, hrp string, data []byte) string {
	t.Helper()
	bits5, err := bech32.ConvertBits(data, 8, 5, true)
	require.NoError(t, err)
	encoded, err := bech32.Encode(hrp, bits5)
	require.NoError(t, err)
	return encoded
}
